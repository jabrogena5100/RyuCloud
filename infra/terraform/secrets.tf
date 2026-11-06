# Container for the CloudFront signing key. Terraform creates only the empty secret; you store
# the private key with the AWS CLI (docs/aws-runbook.md, step 3), so the key never passes
# through Terraform state. API tasks cannot start until the value exists.
resource "aws_secretsmanager_secret" "cloudfront_private_key" {
  name                    = "${local.prefix}/cloudfront-private-key"
  description             = "PEM private key matching the CloudFront public key; signs playback cookies and thumbnail URLs"
  recovery_window_in_days = var.protect_data ? 7 : 0
}
