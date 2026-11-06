# TLS certificates (validated through DNS records in the existing hosted zone) and the
# records that point the app at the load balancer and the media host at CloudFront.
# The hosted zone's name servers must already be delegated from the domain registrar,
# otherwise certificate validation waits until it times out.

resource "aws_acm_certificate" "app" {
  domain_name       = local.app_host
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "app_validation" {
  for_each = {
    for o in aws_acm_certificate.app.domain_validation_options : o.domain_name => {
      name   = o.resource_record_name
      record = o.resource_record_value
      type   = o.resource_record_type
    }
  }

  allow_overwrite = true
  zone_id         = var.hosted_zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
}

resource "aws_acm_certificate_validation" "app" {
  certificate_arn         = aws_acm_certificate.app.arn
  validation_record_fqdns = [for r in aws_route53_record.app_validation : r.fqdn]
}

resource "aws_acm_certificate" "media" {
  provider = aws.us_east_1

  domain_name       = local.media_host
  validation_method = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "media_validation" {
  for_each = {
    for o in aws_acm_certificate.media.domain_validation_options : o.domain_name => {
      name   = o.resource_record_name
      record = o.resource_record_value
      type   = o.resource_record_type
    }
  }

  allow_overwrite = true
  zone_id         = var.hosted_zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
}

resource "aws_acm_certificate_validation" "media" {
  provider = aws.us_east_1

  certificate_arn         = aws_acm_certificate.media.arn
  validation_record_fqdns = [for r in aws_route53_record.media_validation : r.fqdn]
}

resource "aws_route53_record" "app" {
  zone_id = var.hosted_zone_id
  name    = local.app_host
  type    = "A"

  alias {
    name                   = aws_lb.main.dns_name
    zone_id                = aws_lb.main.zone_id
    evaluate_target_health = true
  }
}

resource "aws_route53_record" "media" {
  zone_id = var.hosted_zone_id
  name    = local.media_host
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.media.domain_name
    zone_id                = aws_cloudfront_distribution.media.hosted_zone_id
    evaluate_target_health = false
  }
}
