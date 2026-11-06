output "app_url" {
  description = "Where the app is served once deployed."
  value       = "https://${local.app_host}"
}

output "media_url" {
  description = "Private media host (CloudFront)."
  value       = "https://${local.media_host}"
}

output "aws_region" {
  value = var.aws_region
}

output "ecr_repositories" {
  description = "Repository URLs for the api, worker and frontend images."
  value       = { for k, r in aws_ecr_repository.app : k => r.repository_url }
}

output "ecs_cluster" {
  value = aws_ecs_cluster.main.name
}

output "ecs_services" {
  value = {
    api      = aws_ecs_service.api.name
    frontend = aws_ecs_service.frontend.name
    worker   = aws_ecs_service.worker.name
  }
}

output "task_families" {
  description = "Task definition families (CI registers new revisions of these)."
  value = {
    api      = aws_ecs_task_definition.api.family
    frontend = aws_ecs_task_definition.frontend.family
    worker   = aws_ecs_task_definition.worker.family
    migrate  = aws_ecs_task_definition.migrate.family
  }
}

output "task_subnets" {
  description = "Subnets for one-off tasks such as migrations."
  value       = aws_subnet.public[*].id
}

output "task_security_group" {
  description = "Security group for one-off tasks such as migrations."
  value       = aws_security_group.app.id
}

output "media_bucket" {
  value = aws_s3_bucket.media.id
}

output "jobs_queue_url" {
  value = aws_sqs_queue.jobs.id
}

output "jobs_dlq_url" {
  value = aws_sqs_queue.jobs_dlq.id
}

output "database_endpoint" {
  value = aws_db_instance.main.address
}

output "database_secret_arn" {
  description = "RDS-managed secret holding the master credentials."
  value       = aws_db_instance.main.master_user_secret[0].secret_arn
}

output "cloudfront_private_key_secret" {
  description = "Store the CloudFront private key here (docs/aws-runbook.md, step 3)."
  value       = aws_secretsmanager_secret.cloudfront_private_key.name
}

output "cloudfront_distribution_id" {
  value = aws_cloudfront_distribution.media.id
}

output "github_deploy_role_arn" {
  description = "Set as the AWS_DEPLOY_ROLE_ARN variable of the GitHub 'production' environment."
  value       = local.github_enabled ? aws_iam_role.github_deploy[0].arn : null
}

output "dashboard_url" {
  value = "https://${var.aws_region}.console.aws.amazon.com/cloudwatch/home?region=${var.aws_region}#dashboards:name=${aws_cloudwatch_dashboard.main.dashboard_name}"
}
