# ECS on Fargate. Services: api, frontend (behind the ALB) and worker (queue consumer, no
# inbound traffic). A one-off "migrate" task definition runs database migrations.
#
# Task definitions are owned by Terraform for their SHAPE (CPU, env, secrets, roles), while CI
# owns the IMAGE: it registers a new revision with the git-SHA tag and updates the service.
# Terraform therefore ignores service.task_definition. After changing a task definition here,
# roll it out as described in docs/aws-runbook.md ("Changing task configuration").

resource "aws_ecs_cluster" "main" {
  name = local.prefix

  setting {
    name  = "containerInsights"
    value = "disabled" # Container Insights bills per metric; basic CPU/memory metrics are free
  }
}

resource "aws_ecs_cluster_capacity_providers" "main" {
  cluster_name       = aws_ecs_cluster.main.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]
}

resource "aws_cloudwatch_log_group" "service" {
  for_each = toset(["api", "frontend", "worker", "migrate"])

  name              = "/ecs/${local.prefix}/${each.key}"
  retention_in_days = var.log_retention_days
}

locals {
  image = { for k, r in aws_ecr_repository.app : k => "${r.repository_url}:${var.bootstrap_image_tag}" }

  db_env = [
    { name = "DB_HOST", value = aws_db_instance.main.address },
    { name = "DB_PORT", value = tostring(aws_db_instance.main.port) },
    { name = "DB_NAME", value = aws_db_instance.main.db_name },
    { name = "DB_USER", value = aws_db_instance.main.username },
    { name = "DB_SSLMODE", value = "require" },
  ]

  db_secrets = [
    { name = "DB_PASSWORD", valueFrom = "${aws_db_instance.main.master_user_secret[0].secret_arn}:password::" },
  ]

  # Settings shared by the API and the worker.
  backend_env = concat(local.db_env, [
    { name = "ENVIRONMENT", value = var.environment },
    { name = "LOG_LEVEL", value = "INFO" },
    { name = "AWS_REGION", value = var.aws_region },
    { name = "STORAGE_BACKEND", value = "s3" },
    { name = "S3_BUCKET", value = aws_s3_bucket.media.id },
    { name = "S3_REGION", value = var.aws_region },
    { name = "QUEUE_BACKEND", value = "sqs" },
    { name = "SQS_QUEUE_URL", value = aws_sqs_queue.jobs.id },
    { name = "SQS_VISIBILITY_TIMEOUT_SECONDS", value = tostring(aws_sqs_queue.jobs.visibility_timeout_seconds) },
    { name = "WORKER_MAX_ATTEMPTS", value = "4" },
  ])

  log_config = { for k, g in aws_cloudwatch_log_group.service : k => {
    logDriver = "awslogs"
    options = {
      "awslogs-group"         = g.name
      "awslogs-region"        = var.aws_region
      "awslogs-stream-prefix" = k
    }
  } }
}

# ---------------------------------------------------------------- API

resource "aws_ecs_task_definition" "api" {
  family                   = "${local.prefix}-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.api_cpu
  memory                   = var.api_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.api.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name                   = "api"
    image                  = local.image["api"]
    essential              = true
    readonlyRootFilesystem = true
    portMappings           = [{ containerPort = 8000, protocol = "tcp" }]
    logConfiguration       = local.log_config["api"]

    environment = concat(local.backend_env, [
      { name = "PORT", value = "8000" },
      { name = "COOKIE_SECURE", value = "true" },
      { name = "TRUSTED_ORIGINS", value = "https://${local.app_host}" },
      { name = "ENABLE_API_DOCS", value = "false" },
      { name = "UPLOAD_MAX_BYTES", value = tostring(var.upload_max_bytes) },
      { name = "PLAYBACK_BACKEND", value = "cloudfront" },
      { name = "CLOUDFRONT_DOMAIN", value = local.media_host },
      { name = "CLOUDFRONT_KEY_PAIR_ID", value = aws_cloudfront_public_key.signing.id },
      { name = "CLOUDFRONT_COOKIE_DOMAIN", value = local.app_host },
    ])

    secrets = concat(local.db_secrets, [
      { name = "CLOUDFRONT_PRIVATE_KEY", valueFrom = aws_secretsmanager_secret.cloudfront_private_key.arn },
    ])
  }])
}

resource "aws_ecs_service" "api" {
  name                               = "api"
  cluster                            = aws_ecs_cluster.main.id
  task_definition                    = aws_ecs_task_definition.api.arn
  desired_count                      = var.deploy_services ? var.api_desired_count : 0
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  health_check_grace_period_seconds  = 60

  capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.app.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.api.arn
    container_name   = "api"
    container_port   = 8000
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  lifecycle {
    ignore_changes = [task_definition]
  }

  depends_on = [aws_lb_listener_rule.api, aws_ecs_cluster_capacity_providers.main]
}

# ---------------------------------------------------------------- Frontend

resource "aws_ecs_task_definition" "frontend" {
  family                   = "${local.prefix}-frontend"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.frontend_cpu
  memory                   = var.frontend_memory
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name             = "frontend"
    image            = local.image["frontend"]
    essential        = true
    portMappings     = [{ containerPort = 3000, protocol = "tcp" }]
    logConfiguration = local.log_config["frontend"]
    environment = [
      { name = "NODE_ENV", value = "production" },
    ]
  }])
}

resource "aws_ecs_service" "frontend" {
  name                               = "frontend"
  cluster                            = aws_ecs_cluster.main.id
  task_definition                    = aws_ecs_task_definition.frontend.arn
  desired_count                      = var.deploy_services ? var.frontend_desired_count : 0
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  health_check_grace_period_seconds  = 60

  capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.app.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.frontend.arn
    container_name   = "frontend"
    container_port   = 3000
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  lifecycle {
    ignore_changes = [task_definition]
  }

  depends_on = [aws_lb_listener.https, aws_ecs_cluster_capacity_providers.main]
}

# ---------------------------------------------------------------- Worker

resource "aws_ecs_task_definition" "worker" {
  family                   = "${local.prefix}-worker"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.worker_cpu
  memory                   = var.worker_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.worker.arn

  ephemeral_storage {
    size_in_gib = var.worker_ephemeral_storage_gib
  }

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name             = "worker"
    image            = local.image["worker"]
    essential        = true
    stopTimeout      = 120 # Fargate maximum; the worker hands running jobs back within WORKER_SHUTDOWN_GRACE_SECONDS
    logConfiguration = local.log_config["worker"]

    environment = concat(local.backend_env, [
      { name = "WORKER_CONCURRENCY", value = "1" },
      { name = "FFMPEG_THREADS", value = tostring(max(1, floor(var.worker_cpu / 1024))) },
      { name = "WORKER_SHUTDOWN_GRACE_SECONDS", value = "90" },
    ])

    secrets = local.db_secrets
  }])
}

resource "aws_ecs_service" "worker" {
  name                               = "worker"
  cluster                            = aws_ecs_cluster.main.id
  task_definition                    = aws_ecs_task_definition.worker.arn
  desired_count                      = var.deploy_services ? var.worker_desired_count : 0
  deployment_minimum_healthy_percent = 0 # replace in place: no second (billed) task during a deploy
  deployment_maximum_percent         = 100

  capacity_provider_strategy {
    capacity_provider = var.worker_use_spot ? "FARGATE_SPOT" : "FARGATE"
    weight            = 1
  }

  network_configuration {
    subnets          = aws_subnet.public[*].id
    security_groups  = [aws_security_group.app.id]
    assign_public_ip = true
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  lifecycle {
    ignore_changes = [task_definition]
  }

  depends_on = [aws_ecs_cluster_capacity_providers.main]
}

# ---------------------------------------------------------------- Migrations (one-off task)

# Runs `api migrate up` against RDS. Deliberately minimal: database credentials only, no
# storage/queue/CDN settings and no task role. Run it with scripts/aws-run-migrations.sh.
resource "aws_ecs_task_definition" "migrate" {
  family                   = "${local.prefix}-migrate"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = aws_iam_role.execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name             = "migrate"
    image            = local.image["api"]
    essential        = true
    command          = ["migrate", "up"]
    logConfiguration = local.log_config["migrate"]
    environment      = local.db_env
    secrets          = local.db_secrets
  }])
}
