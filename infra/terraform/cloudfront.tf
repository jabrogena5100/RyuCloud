# Private video delivery.
#   - The bucket is private; CloudFront reads it with Origin Access Control (SigV4).
#   - Every request must carry a valid CloudFront signature from the trusted key group:
#       playback   signed cookies set by the API for one video (HLS playlists reference
#                  segments by relative path, so a per-URL signature would not cover them)
#       thumbnail  signed URL per image
#   - The private key lives in Secrets Manager (see secrets.tf); only the public half is here.

resource "aws_cloudfront_public_key" "signing" {
  name        = "${local.prefix}-signing"
  comment     = "Verifies signed cookies and URLs issued by the API"
  encoded_key = file(var.cloudfront_public_key_path)
}

resource "aws_cloudfront_key_group" "signing" {
  name    = "${local.prefix}-signing"
  comment = "Keys trusted to authorise media access"
  items   = [aws_cloudfront_public_key.signing.id]
}

resource "aws_cloudfront_origin_access_control" "media" {
  name                              = "${local.prefix}-media"
  description                       = "CloudFront to the private media bucket"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# The browser loads playlists and segments from media.<domain> while the page is on <domain>,
# so the CDN must send CORS headers that allow credentialed requests from the app origin.
resource "aws_cloudfront_response_headers_policy" "media" {
  name    = "${local.prefix}-media"
  comment = "CORS for credentialed HLS requests from the app origin"

  cors_config {
    access_control_allow_credentials = true
    origin_override                  = true

    access_control_allow_headers {
      items = ["Range", "Accept", "Content-Type"]
    }

    access_control_allow_methods {
      items = ["GET", "HEAD"]
    }

    access_control_allow_origins {
      items = ["https://${local.app_host}"]
    }

    access_control_expose_headers {
      items = ["Content-Length", "Content-Range", "Content-Type"]
    }

    access_control_max_age_sec = 3000
  }

  security_headers_config {
    content_type_options {
      override = true
    }

    strict_transport_security {
      access_control_max_age_sec = 31536000
      include_subdomains         = true
      override                   = true
    }
  }
}

resource "aws_cloudfront_distribution" "media" {
  comment             = "${local.prefix} private video delivery"
  enabled             = true
  is_ipv6_enabled     = true
  http_version        = "http2and3"
  price_class         = var.cloudfront_price_class
  aliases             = [local.media_host]
  wait_for_deployment = true

  origin {
    origin_id                = "media-bucket"
    domain_name              = aws_s3_bucket.media.bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.media.id
  }

  default_cache_behavior {
    target_origin_id       = "media-bucket"
    viewer_protocol_policy = "https-only"
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    compress               = true

    # AWS managed policy "CachingOptimized": cookies, query strings and Origin are not part
    # of the cache key, so the per-viewer signatures do not fragment the cache.
    cache_policy_id            = "658327ea-f89d-4fab-a63d-7e88639e58f6"
    response_headers_policy_id = aws_cloudfront_response_headers_policy.media.id

    trusted_key_groups = [aws_cloudfront_key_group.signing.id]
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    acm_certificate_arn      = aws_acm_certificate_validation.media.certificate_arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }
}
