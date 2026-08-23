# These outputs are the stack's public interface: stacks 20-data and 30-eks read them through
# terraform_remote_state. Renaming or removing one breaks a downstream plan, so treat them as a
# contract and add rather than rename.

output "vpc_id" {
  description = "VPC id."
  value       = module.network.vpc_id
}

output "vpc_cidr_block" {
  description = "VPC CIDR block."
  value       = module.network.vpc_cidr_block
}

output "azs" {
  description = "Availability zones the subnets live in, in order."
  value       = module.network.azs
}

output "public_subnet_ids" {
  description = "Public subnets: NAT gateway today, internet-facing ALB from Phase 12."
  value       = module.network.public_subnet_ids
}

output "private_subnet_ids" {
  description = "Private subnets with a NAT route: EKS nodes, MSK brokers, Kafka Connect."
  value       = module.network.private_subnet_ids
}

output "data_subnet_ids" {
  description = "Data subnets: no NAT route, no internet gateway route. Both RDS instances live here."
  value       = module.network.data_subnet_ids
}

output "data_subnet_group_name" {
  description = "RDS DB subnet group spanning the data subnets."
  value       = module.network.data_subnet_group_name
}

output "private_route_table_ids" {
  description = "Private-tier route table ids."
  value       = module.network.private_route_table_ids
}

output "data_route_table_ids" {
  description = "Data-tier route table ids. The isolation claim is verified by asserting these carry no 0.0.0.0/0 route."
  value       = module.network.data_route_table_ids
}

output "nat_gateway_ids" {
  description = "NAT gateway ids -- exactly one while single_nat_gateway is true."
  value       = module.network.nat_gateway_ids
}

output "nat_public_ips" {
  description = "The platform's egress IP addresses, for any third party that asks for an allowlist entry."
  value       = module.network.nat_public_ips
}

output "s3_gateway_endpoint_id" {
  description = "S3 gateway VPC endpoint id."
  value       = module.network.s3_gateway_endpoint_id
}
