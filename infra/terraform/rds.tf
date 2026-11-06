# PostgreSQL on RDS. Single-AZ by default (see var.db_multi_az). The master password is
# generated and stored by RDS in Secrets Manager (manage_master_user_password), so it never
# appears in Terraform state, variables or the repository. ECS injects it into the tasks.

resource "aws_db_subnet_group" "main" {
  name       = local.prefix
  subnet_ids = aws_subnet.db[*].id
}

resource "aws_db_parameter_group" "main" {
  name   = "${local.prefix}-postgres17"
  family = "postgres17"

  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }

  parameter {
    name  = "log_min_duration_statement"
    value = "1000" # log statements slower than 1 s
  }
}

resource "aws_db_instance" "main" {
  identifier = local.prefix

  engine               = "postgres"
  engine_version       = "17"
  instance_class       = var.db_instance_class
  parameter_group_name = aws_db_parameter_group.main.name

  db_name  = "ryucloud"
  username = "ryucloud"

  manage_master_user_password = true

  allocated_storage     = var.db_allocated_storage_gib
  max_allocated_storage = var.db_max_allocated_storage_gib
  storage_type          = "gp3"
  storage_encrypted     = true

  multi_az               = var.db_multi_az
  db_subnet_group_name   = aws_db_subnet_group.main.name
  vpc_security_group_ids = [aws_security_group.db.id]
  publicly_accessible    = false

  backup_retention_period    = var.db_backup_retention_days
  copy_tags_to_snapshot      = true
  auto_minor_version_upgrade = true

  deletion_protection       = var.protect_data
  skip_final_snapshot       = !var.protect_data
  final_snapshot_identifier = var.protect_data ? "${local.prefix}-final" : null

  enabled_cloudwatch_logs_exports = ["postgresql"]
}
