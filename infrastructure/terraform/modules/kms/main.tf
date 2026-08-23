# Customer managed KMS keys, one per blast-radius domain (data / db / msk / secrets).
#
# Key policies are deliberately the AWS default shape: the account root is given full
# access so that *IAM* policies on consuming principals decide who may use the key. That
# keeps authorization in one place (the IRSA role or the CI role) instead of splitting it
# between a key policy and an IAM policy, which is the usual source of "AccessDenied but
# the IAM policy clearly allows it" incidents. Cross-account grants (none in dev) would be
# the one case that needs a key-policy statement instead.

resource "aws_kms_key" "this" {
  for_each = var.keys

  description             = each.value.description
  enable_key_rotation     = each.value.enable_key_rotation
  deletion_window_in_days = each.value.deletion_window_in_days
  multi_region            = each.value.multi_region

  tags = {
    Name = "${var.name_prefix}-${each.key}"
  }
}

resource "aws_kms_alias" "this" {
  for_each = var.keys

  name          = "alias/${var.name_prefix}-${each.key}"
  target_key_id = aws_kms_key.this[each.key].key_id
}
