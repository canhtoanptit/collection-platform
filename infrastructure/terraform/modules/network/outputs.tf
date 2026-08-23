output "vpc_id" {
  description = "VPC id."
  value       = module.vpc.vpc_id
}

output "vpc_cidr_block" {
  description = "VPC CIDR block, used to scope security-group rules that must cover the whole VPC."
  value       = module.vpc.vpc_cidr_block
}

output "azs" {
  description = "The availability zones the subnets were created in, in order."
  value       = module.vpc.azs
}

output "public_subnet_ids" {
  description = "Public subnet ids (NAT gateway today, internet-facing ALB in Phase 12)."
  value       = module.vpc.public_subnets
}

output "private_subnet_ids" {
  description = "Private subnet ids with a NAT route: EKS nodes, MSK brokers, Kafka Connect."
  value       = module.vpc.private_subnets
}

output "data_subnet_ids" {
  description = "Data subnet ids: no NAT route, no internet gateway route. RDS lives here."
  value       = module.vpc.database_subnets
}

output "data_subnet_group_name" {
  description = "RDS DB subnet group spanning the data subnets, created by the wrapped module."
  value       = module.vpc.database_subnet_group_name
}

output "private_route_table_ids" {
  description = "Route table ids for the private tier."
  value       = module.vpc.private_route_table_ids
}

output "data_route_table_ids" {
  description = "Route table ids for the data tier. Asserting these carry no 0.0.0.0/0 route is how the isolation is verified."
  value       = module.vpc.database_route_table_ids
}

output "nat_gateway_ids" {
  description = "NAT gateway ids. Exactly one in dev (single_nat_gateway)."
  value       = module.vpc.natgw_ids
}

output "nat_public_ips" {
  description = "Elastic IPs of the NAT gateway(s). This is the platform's egress address, needed when a third party asks for an allowlist entry."
  value       = module.vpc.nat_public_ips
}

output "default_security_group_id" {
  description = "The VPC's default security group, adopted and emptied of all rules. Nothing should use it."
  value       = module.vpc.default_security_group_id
}

output "s3_gateway_endpoint_id" {
  description = "S3 gateway VPC endpoint id, or null when disabled."
  value       = try(aws_vpc_endpoint.s3[0].id, null)
}
