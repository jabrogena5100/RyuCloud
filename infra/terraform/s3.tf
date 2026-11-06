# One private bucket for raw uploads and processed renditions (same key layout as local MinIO):
#   uploads/{video}/{upload}/source   original upload   (browser PUT via presigned URL; never served)
#   videos/{video}/hls/...            HLS renditions    (served only through CloudFront)
#   videos/{video}/thumbnail.jpg

resource "aws_s3_bucket" "media" {
  bucket        = "${local.prefix}-media-${local.account_id}"
  force_destroy = !var.protect_data
}

resource "aws_s3_bucket_public_access_block" "media" {
  bucket                  = aws_s3_bucket.media.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "media" {
  bucket = aws_s3_bucket.media.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# The browser PUTs the file straight to S3 from the app origin, so S3 must allow that origin.
resource "aws_s3_bucket_cors_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  cors_rule {
    allowed_origins = ["https://${local.app_host}"]
    allowed_methods = ["PUT"]
    allowed_headers = ["*"]
    expose_headers  = ["ETag"]
    max_age_seconds = 3000
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  rule {
    id     = "abort-incomplete-multipart"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  dynamic "rule" {
    for_each = var.source_retention_days > 0 ? [1] : []

    content {
      id     = "expire-original-uploads"
      status = "Enabled"

      filter {
        prefix = "uploads/"
      }

      expiration {
        days = var.source_retention_days
      }
    }
  }
}

data "aws_iam_policy_document" "media_bucket" {
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [aws_s3_bucket.media.arn, "${aws_s3_bucket.media.arn}/*"]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  # CloudFront (via Origin Access Control) may read processed output only, and only
  # on behalf of this distribution. Original uploads are not reachable from the CDN.
  statement {
    sid       = "AllowCloudFrontReadProcessedMedia"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.media.arn}/videos/*"]

    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.media.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "media" {
  bucket = aws_s3_bucket.media.id
  policy = data.aws_iam_policy_document.media_bucket.json

  depends_on = [aws_s3_bucket_public_access_block.media]
}
