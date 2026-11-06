terraform {
  required_version = ">= 1.10.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.81, < 7.0"
    }
  }

  # Remote state. The bucket is created once by hand (see docs/aws-runbook.md, step 2) and
  # supplied at init time: terraform init -backend-config=backend.hcl
  # `terraform init -backend=false` is enough for fmt/validate with no AWS access.
  backend "s3" {}
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = local.tags
  }
}

# CloudFront only accepts ACM certificates from us-east-1.
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"

  default_tags {
    tags = local.tags
  }
}
