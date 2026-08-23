# Cross-stack reads.
#
# Five independent stacks (ADR-0010) means cross-stack outputs, and the coupling has to be explicit
# because it is no longer visible in one graph. This stack reads 10-network and nothing else, so
# the apply order is 10-network -> 20-data with no cycle.
#
# The state bucket is a variable rather than a literal because it embeds the account id. It must
# match `bucket` in envs/dev/backend.hcl: the backend configuration and this data source are two
# separate mechanisms, and nothing reconciles them automatically. scripts/verify/INF-A.sh asserts
# the two files agree.
data "terraform_remote_state" "network" {
  backend = "s3"

  config = {
    bucket = var.state_bucket
    key    = var.network_state_key
    region = var.region
  }
}

locals {
  vpc_id                 = data.terraform_remote_state.network.outputs.vpc_id
  private_subnet_ids     = data.terraform_remote_state.network.outputs.private_subnet_ids
  data_subnet_group_name = data.terraform_remote_state.network.outputs.data_subnet_group_name

  name_prefix = "${var.project}-${var.env}"

  # Empty until stacks/30-eks exists. compact() turns the null default into an empty list, which
  # every consumer reads as "create no ingress rules".
  eks_client_security_group_ids = compact([var.eks_node_security_group_id])
}
