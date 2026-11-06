locals {
  repositories = toset(["api", "worker", "frontend"])
}

resource "aws_ecr_repository" "app" {
  for_each = local.repositories

  name                 = "${local.prefix}/${each.key}"
  image_tag_mutability = "IMMUTABLE" # a tag always means the same image; CI pushes git SHAs
  force_delete         = !var.protect_data

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }
}

resource "aws_ecr_lifecycle_policy" "app" {
  for_each   = aws_ecr_repository.app
  repository = each.value.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the 15 most recent images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 15
      }
      action = { type = "expire" }
    }]
  })
}
