# The platform VPC: three subnet tiers across two AZs, one NAT gateway, and an S3 gateway
# endpoint so the ingestion and analytics S3 traffic never pays NAT data-processing charges.
#
# This wraps terraform-aws-modules/vpc at an exact version rather than reimplementing subnets,
# route tables and NAT plumbing. The wrapper exists so the rest of the repo depends on *our*
# vocabulary ("data subnets") and not on the upstream module's ("database subnets"), and so a
# module upgrade is one version bump reviewed in one place.

data "aws_availability_zones" "available" {
  state = "available"

  # Exclude local zones and wavelength zones: they cannot host RDS, MSK or EKS node groups, and
  # they sort into the middle of the list, so taking the first N without this filter is a
  # apply-time failure that looks like a quota problem.
  filter {
    name   = "opt-in-status"
    values = ["opt-in-not-required"]
  }
}

locals {
  azs = slice(sort(data.aws_availability_zones.available.names), 0, var.az_count)

  # /20 per subnet with a 4-index offset per tier: public 0-3, private 4-7, data 8-11, 12-15
  # spare. Adding an AZ therefore never renumbers an existing subnet, which would otherwise
  # destroy and recreate every subnet in the VPC.
  public_subnets   = [for i in range(var.az_count) : cidrsubnet(var.vpc_cidr, 4, i)]
  private_subnets  = [for i in range(var.az_count) : cidrsubnet(var.vpc_cidr, 4, 4 + i)]
  database_subnets = [for i in range(var.az_count) : cidrsubnet(var.vpc_cidr, 4, 8 + i)]

  cluster_tag = var.eks_cluster_name == null ? {} : {
    "kubernetes.io/cluster/${var.eks_cluster_name}" = "shared"
  }
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "6.7.0"

  name = var.name
  cidr = var.vpc_cidr
  azs  = local.azs

  public_subnets   = local.public_subnets
  private_subnets  = local.private_subnets
  database_subnets = local.database_subnets

  public_subnet_suffix   = "public"
  private_subnet_suffix  = "private"
  database_subnet_suffix = "data"

  # Required for RDS endpoint resolution and for the S3 gateway endpoint's private DNS.
  enable_dns_hostnames = true
  enable_dns_support   = true

  enable_nat_gateway     = var.enable_nat_gateway
  single_nat_gateway     = var.single_nat_gateway
  one_nat_gateway_per_az = false

  # The data tier must have *no* route to the NAT gateway or the internet gateway: its own route
  # table, and no default route added to it. Without create_database_subnet_route_table the
  # upstream module associates the data subnets with the private route tables, which do have a
  # NAT route -- silently defeating the isolation this tier exists for.
  create_database_subnet_route_table     = true
  create_database_nat_gateway_route      = false
  create_database_internet_gateway_route = false
  create_database_subnet_group           = true
  database_subnet_group_name             = "${var.name}-data"

  # No public IPs on launch: the public subnets exist for the NAT gateway today and an ALB in
  # Phase 12. Nothing in them should be individually reachable.
  map_public_ip_on_launch = false

  # Adopt the default security group and strip every rule from it, so a resource created without
  # an explicit security group is isolated instead of wide open inside the VPC.
  manage_default_security_group  = true
  default_security_group_name    = "${var.name}-default-do-not-use"
  default_security_group_ingress = []
  default_security_group_egress  = []

  enable_flow_log = var.enable_flow_log

  public_subnet_tags = merge(local.cluster_tag, {
    tier = "public"
    # Tells the AWS Load Balancer Controller which subnets may host internet-facing load
    # balancers. Set now so Phase 12's ingress is a flag flip, not a network change.
    "kubernetes.io/role/elb" = "1"
  })

  private_subnet_tags = merge(local.cluster_tag, {
    tier                              = "private"
    "kubernetes.io/role/internal-elb" = "1"
  })

  database_subnet_tags = {
    tier = "data"
  }

  vpc_tags = {
    Name = var.name
  }
}

# Gateway endpoints are free and are attached to route tables rather than to subnets. Every route
# table gets it: the private tables so pods, Kafka Connect and Airflow reach S3 (and pull ECR
# layers, which are S3 objects) without NAT data-processing charges; the data tables so a future
# RDS export or Snowflake unload has a path; the public table for symmetry.
resource "aws_vpc_endpoint" "s3" {
  count = var.enable_s3_gateway_endpoint ? 1 : 0

  vpc_id            = module.vpc.vpc_id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"

  route_table_ids = concat(
    module.vpc.private_route_table_ids,
    module.vpc.database_route_table_ids,
    module.vpc.public_route_table_ids,
  )

  tags = {
    Name = "${var.name}-s3-gateway"
  }
}
