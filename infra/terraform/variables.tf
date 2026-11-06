# ---------------------------------------------------------------------------------------
# Identity
# ---------------------------------------------------------------------------------------

variable "project" {
  description = "Short name used as a prefix for every resource."
  type        = string
  default     = "ryucloud"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,19}$", var.project))
    error_message = "project must be 2-20 characters: lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "environment" {
  description = "Deployment environment."
  type        = string
  default     = "prod"

  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "environment must be dev, staging or prod."
  }
}

variable "aws_region" {
  description = "Region for everything except the CloudFront certificate (always us-east-1)."
  type        = string
  default     = "us-west-2"

  validation {
    condition     = can(regex("^[a-z]{2}-[a-z]+-[0-9]$", var.aws_region))
    error_message = "aws_region must look like us-west-2."
  }
}

# ---------------------------------------------------------------------------------------
# DNS and access
# ---------------------------------------------------------------------------------------

variable "domain_name" {
  description = "Host name of the app, for example ryucloud.example.com. Media is served from media.<domain_name>. The signed CloudFront cookies need both hosts under one parent domain."
  type        = string

  validation {
    condition     = can(regex("^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z]{2,}$", var.domain_name))
    error_message = "domain_name must be a lowercase DNS name such as ryucloud.example.com (no scheme, no trailing dot)."
  }
}

variable "hosted_zone_id" {
  description = "ID of the existing Route 53 public hosted zone that contains domain_name."
  type        = string

  validation {
    condition     = can(regex("^Z[A-Z0-9]{6,32}$", var.hosted_zone_id))
    error_message = "hosted_zone_id must look like Z0123456789ABCDEFGHIJ."
  }
}

variable "alert_email" {
  description = "Address that receives CloudWatch alarms and budget alerts. AWS sends a confirmation email you must accept."
  type        = string

  validation {
    condition     = can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", var.alert_email))
    error_message = "alert_email must be a valid email address."
  }
}

variable "cloudfront_public_key_path" {
  description = "Path to the PEM public key CloudFront uses to verify signed cookies/URLs. The matching private key is stored in Secrets Manager by hand and never enters Terraform state."
  type        = string
  default     = "cloudfront_public_key.pem"

  validation {
    condition     = fileexists(var.cloudfront_public_key_path) && strcontains(file(var.cloudfront_public_key_path), "BEGIN PUBLIC KEY")
    error_message = "cloudfront_public_key_path must point to a PEM file containing a 'BEGIN PUBLIC KEY' block (see docs/aws-runbook.md, step 3)."
  }
}

# ---------------------------------------------------------------------------------------
# Network
# ---------------------------------------------------------------------------------------

variable "vpc_cidr" {
  description = "CIDR block of the VPC. Two public and two isolated database subnets are carved out of it."
  type        = string
  default     = "10.20.0.0/16"

  validation {
    condition     = can(cidrhost(var.vpc_cidr, 0)) && tonumber(split("/", var.vpc_cidr)[1]) <= 20
    error_message = "vpc_cidr must be a valid CIDR with a prefix of /20 or larger."
  }
}

# ---------------------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------------------

variable "db_instance_class" {
  description = "RDS instance class. db.t4g.micro is the smallest sensible choice for a portfolio."
  type        = string
  default     = "db.t4g.micro"

  validation {
    condition     = startswith(var.db_instance_class, "db.")
    error_message = "db_instance_class must start with db. (for example db.t4g.micro)."
  }
}

variable "db_allocated_storage_gib" {
  description = "Initial gp3 storage in GiB."
  type        = number
  default     = 20

  validation {
    condition     = var.db_allocated_storage_gib >= 20 && var.db_allocated_storage_gib <= 1000
    error_message = "db_allocated_storage_gib must be between 20 and 1000."
  }
}

variable "db_max_allocated_storage_gib" {
  description = "Upper bound for storage autoscaling (also a cost ceiling)."
  type        = number
  default     = 50

  validation {
    condition     = var.db_max_allocated_storage_gib >= var.db_allocated_storage_gib
    error_message = "db_max_allocated_storage_gib must be at least db_allocated_storage_gib."
  }
}

variable "db_multi_az" {
  description = "Run a synchronous standby in a second AZ. Roughly doubles the database cost; off by default."
  type        = bool
  default     = false
}

variable "db_backup_retention_days" {
  description = "Days of automated backups (point-in-time recovery). 0 disables backups."
  type        = number
  default     = 7

  validation {
    condition     = var.db_backup_retention_days >= 0 && var.db_backup_retention_days <= 35
    error_message = "db_backup_retention_days must be between 0 and 35."
  }
}

variable "protect_data" {
  description = "Deletion protection and a final snapshot for RDS, and no force-destroy of the media bucket. Set to false only to tear down a throwaway environment."
  type        = bool
  default     = true
}

# ---------------------------------------------------------------------------------------
# Compute
# ---------------------------------------------------------------------------------------

variable "deploy_services" {
  description = "false creates the cluster and everything else but runs zero tasks, which is the safe first apply: the images do not exist in ECR yet. Set to true after pushing images (docs/aws-runbook.md)."
  type        = bool
  default     = false
}

variable "bootstrap_image_tag" {
  description = "Image tag written into the initial task definitions. CI registers new revisions with the real tag afterwards (Terraform ignores those changes)."
  type        = string
  default     = "bootstrap"
}

variable "api_cpu" {
  description = "Fargate CPU units for the API task."
  type        = number
  default     = 256

  validation {
    condition     = contains([256, 512, 1024, 2048, 4096], var.api_cpu)
    error_message = "api_cpu must be one of 256, 512, 1024, 2048, 4096."
  }
}

variable "api_memory" {
  description = "Fargate memory (MiB) for the API task. Must be a valid pairing with api_cpu."
  type        = number
  default     = 512
}

variable "frontend_cpu" {
  description = "Fargate CPU units for the Next.js task."
  type        = number
  default     = 256

  validation {
    condition     = contains([256, 512, 1024, 2048, 4096], var.frontend_cpu)
    error_message = "frontend_cpu must be one of 256, 512, 1024, 2048, 4096."
  }
}

variable "frontend_memory" {
  description = "Fargate memory (MiB) for the Next.js task. Must be a valid pairing with frontend_cpu."
  type        = number
  default     = 512
}

variable "worker_cpu" {
  description = "Fargate CPU units for the worker task (FFmpeg is CPU-bound)."
  type        = number
  default     = 1024

  validation {
    condition     = contains([512, 1024, 2048, 4096], var.worker_cpu)
    error_message = "worker_cpu must be one of 512, 1024, 2048, 4096."
  }
}

variable "worker_memory" {
  description = "Fargate memory (MiB) for the worker task. Must be a valid pairing with worker_cpu."
  type        = number
  default     = 2048
}

variable "worker_ephemeral_storage_gib" {
  description = "Scratch disk for the worker (source download plus transcoded output)."
  type        = number
  default     = 30

  validation {
    condition     = var.worker_ephemeral_storage_gib >= 21 && var.worker_ephemeral_storage_gib <= 200
    error_message = "worker_ephemeral_storage_gib must be between 21 and 200."
  }
}

variable "worker_use_spot" {
  description = "Run the worker on Fargate Spot (about 70% cheaper). Safe because jobs are retried and the worker hands jobs back on SIGTERM."
  type        = bool
  default     = true
}

variable "api_desired_count" {
  description = "Number of API tasks."
  type        = number
  default     = 1

  validation {
    condition     = var.api_desired_count >= 1 && var.api_desired_count <= 10
    error_message = "api_desired_count must be between 1 and 10."
  }
}

variable "frontend_desired_count" {
  description = "Number of Next.js tasks."
  type        = number
  default     = 1

  validation {
    condition     = var.frontend_desired_count >= 1 && var.frontend_desired_count <= 10
    error_message = "frontend_desired_count must be between 1 and 10."
  }
}

variable "worker_desired_count" {
  description = "Number of worker tasks. Each runs WORKER_CONCURRENCY=1 FFmpeg job at a time."
  type        = number
  default     = 1

  validation {
    condition     = var.worker_desired_count >= 1 && var.worker_desired_count <= 10
    error_message = "worker_desired_count must be between 1 and 10."
  }
}

variable "upload_max_bytes" {
  description = "Largest accepted upload (UPLOAD_MAX_BYTES)."
  type        = number
  default     = 1073741824

  validation {
    condition     = var.upload_max_bytes >= 1048576 && var.upload_max_bytes <= 5368709120
    error_message = "upload_max_bytes must be between 1 MiB and 5 GiB (the single-request S3 limit)."
  }
}

# ---------------------------------------------------------------------------------------
# Storage, queue, CDN
# ---------------------------------------------------------------------------------------

variable "source_retention_days" {
  description = "Delete original uploads (uploads/ prefix) after this many days. 0 keeps them forever. Processed HLS output is never expired."
  type        = number
  default     = 0

  validation {
    condition     = var.source_retention_days == 0 || var.source_retention_days >= 7
    error_message = "source_retention_days must be 0 (keep) or at least 7."
  }
}

variable "sqs_max_receive_count" {
  description = "Deliveries of one message before SQS moves it to the dead-letter queue."
  type        = number
  default     = 5

  validation {
    condition     = var.sqs_max_receive_count >= 1 && var.sqs_max_receive_count <= 20
    error_message = "sqs_max_receive_count must be between 1 and 20."
  }
}

variable "cloudfront_price_class" {
  description = "PriceClass_100 (North America and Europe) is the cheapest."
  type        = string
  default     = "PriceClass_100"

  validation {
    condition     = contains(["PriceClass_100", "PriceClass_200", "PriceClass_All"], var.cloudfront_price_class)
    error_message = "cloudfront_price_class must be PriceClass_100, PriceClass_200 or PriceClass_All."
  }
}

# ---------------------------------------------------------------------------------------
# Observability, cost, CI
# ---------------------------------------------------------------------------------------

variable "log_retention_days" {
  description = "CloudWatch Logs retention."
  type        = number
  default     = 14

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365], var.log_retention_days)
    error_message = "log_retention_days must be a retention value CloudWatch accepts (1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365)."
  }
}

variable "monthly_budget_usd" {
  description = "Monthly AWS budget; an email is sent at 80% actual and 100% forecast."
  type        = number
  default     = 150

  validation {
    condition     = var.monthly_budget_usd > 0
    error_message = "monthly_budget_usd must be greater than zero."
  }
}

variable "github_repository" {
  description = "owner/name of the GitHub repository allowed to deploy through OIDC. Empty skips creating the deploy role."
  type        = string
  default     = ""

  validation {
    condition     = var.github_repository == "" || can(regex("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$", var.github_repository))
    error_message = "github_repository must be empty or look like owner/name."
  }
}

variable "create_github_oidc_provider" {
  description = "Create the account-wide GitHub OIDC provider. Set to false if your account already has one (there can only be one)."
  type        = bool
  default     = true
}
