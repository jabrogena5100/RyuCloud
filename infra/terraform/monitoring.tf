# Observability. Both Go binaries already write one JSON object per line to stdout
# (log/slog), which ECS ships to CloudWatch Logs. This file turns that into numbers and alerts:
#   - metric filters count error logs and job outcomes from the structured log fields
#   - alarms cover the load balancer, ECS, SQS (including the dead-letter queue) and RDS
#   - a dashboard puts the key graphs on one page
#   - a budget emails you before the bill surprises you
# Alarms are billed ($0.10 each per month) and metric filters create custom metrics ($0.30
# each per month); see docs/aws-costs.md.

# Not encrypted with a KMS key on purpose: CloudWatch alarms cannot publish to a topic that
# uses the AWS-managed aws/sns key, so alerts would silently never arrive. The messages carry
# alarm names and metric values only.
resource "aws_sns_topic" "alerts" {
  name = "${local.prefix}-alerts"
}

resource "aws_sns_topic_subscription" "email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

# ---------------------------------------------------------------- log-derived metrics

locals {
  log_metrics = {
    api_errors = {
      group   = aws_cloudwatch_log_group.service["api"].name
      pattern = "{ $.level = \"ERROR\" }"
      name    = "ApiErrors"
    }
    worker_errors = {
      group   = aws_cloudwatch_log_group.service["worker"].name
      pattern = "{ $.level = \"ERROR\" }"
      name    = "WorkerErrors"
    }
    jobs_succeeded = {
      group   = aws_cloudwatch_log_group.service["worker"].name
      pattern = "{ $.msg = \"job succeeded\" }"
      name    = "JobsSucceeded"
    }
    jobs_failed = {
      group   = aws_cloudwatch_log_group.service["worker"].name
      pattern = "{ ($.msg = \"job failed permanently\") || ($.msg = \"job failed: attempts exhausted\") }"
      name    = "JobsFailed"
    }
  }

  metric_namespace = "RyuCloud/${var.environment}"
}

resource "aws_cloudwatch_log_metric_filter" "app" {
  for_each = local.log_metrics

  name           = "${local.prefix}-${each.key}"
  log_group_name = each.value.group
  pattern        = each.value.pattern

  metric_transformation {
    name      = each.value.name
    namespace = local.metric_namespace
    value     = "1"
  }
}

# ---------------------------------------------------------------- alarms

locals {
  alarm_actions = [aws_sns_topic.alerts.arn]

  # Simple "metric over threshold" alarms. Missing data means "nothing happened" for count
  # metrics, so it must not trigger an alarm.
  threshold_alarms = {
    alb-5xx = {
      description = "The load balancer itself returned 5xx responses"
      namespace   = "AWS/ApplicationELB"
      metric      = "HTTPCode_ELB_5XX_Count"
      statistic   = "Sum"
      threshold   = 5
      operator    = "GreaterThanOrEqualToThreshold"
      periods     = 1
      dimensions  = { LoadBalancer = aws_lb.main.arn_suffix }
    }
    api-5xx = {
      description = "The API returned 5xx responses"
      namespace   = "AWS/ApplicationELB"
      metric      = "HTTPCode_Target_5XX_Count"
      statistic   = "Sum"
      threshold   = 10
      operator    = "GreaterThanOrEqualToThreshold"
      periods     = 1
      dimensions  = { LoadBalancer = aws_lb.main.arn_suffix, TargetGroup = aws_lb_target_group.api.arn_suffix }
    }
    api-unhealthy-hosts = {
      description = "A load balancer target of the API is failing health checks"
      namespace   = "AWS/ApplicationELB"
      metric      = "UnHealthyHostCount"
      statistic   = "Maximum"
      threshold   = 1
      operator    = "GreaterThanOrEqualToThreshold"
      periods     = 2
      dimensions  = { LoadBalancer = aws_lb.main.arn_suffix, TargetGroup = aws_lb_target_group.api.arn_suffix }
    }
    frontend-unhealthy-hosts = {
      description = "A load balancer target of the frontend is failing health checks"
      namespace   = "AWS/ApplicationELB"
      metric      = "UnHealthyHostCount"
      statistic   = "Maximum"
      threshold   = 1
      operator    = "GreaterThanOrEqualToThreshold"
      periods     = 2
      dimensions  = { LoadBalancer = aws_lb.main.arn_suffix, TargetGroup = aws_lb_target_group.frontend.arn_suffix }
    }
    dlq-not-empty = {
      description = "A job message was moved to the dead-letter queue: inspect it (docs/aws-runbook.md)"
      namespace   = "AWS/SQS"
      metric      = "ApproximateNumberOfMessagesVisible"
      statistic   = "Maximum"
      threshold   = 1
      operator    = "GreaterThanOrEqualToThreshold"
      periods     = 1
      dimensions  = { QueueName = aws_sqs_queue.jobs_dlq.name }
    }
    jobs-queue-stale = {
      description = "The oldest job has waited over 30 minutes: the worker may be down or overloaded"
      namespace   = "AWS/SQS"
      metric      = "ApproximateAgeOfOldestMessage"
      statistic   = "Maximum"
      threshold   = 1800
      operator    = "GreaterThanOrEqualToThreshold"
      periods     = 2
      dimensions  = { QueueName = aws_sqs_queue.jobs.name }
    }
    rds-cpu = {
      description = "Database CPU above 80%"
      namespace   = "AWS/RDS"
      metric      = "CPUUtilization"
      statistic   = "Average"
      threshold   = 80
      operator    = "GreaterThanOrEqualToThreshold"
      periods     = 3
      dimensions  = { DBInstanceIdentifier = aws_db_instance.main.identifier }
    }
    rds-free-storage = {
      description = "Database has under 3 GiB of free storage"
      namespace   = "AWS/RDS"
      metric      = "FreeStorageSpace"
      statistic   = "Minimum"
      threshold   = 3221225472
      operator    = "LessThanOrEqualToThreshold"
      periods     = 1
      dimensions  = { DBInstanceIdentifier = aws_db_instance.main.identifier }
    }
    rds-freeable-memory = {
      description = "Database has under 100 MiB of freeable memory"
      namespace   = "AWS/RDS"
      metric      = "FreeableMemory"
      statistic   = "Minimum"
      threshold   = 104857600
      operator    = "LessThanOrEqualToThreshold"
      periods     = 3
      dimensions  = { DBInstanceIdentifier = aws_db_instance.main.identifier }
    }
    rds-connections = {
      description = "Database has over 60 connections (db.t4g.micro allows roughly 80)"
      namespace   = "AWS/RDS"
      metric      = "DatabaseConnections"
      statistic   = "Maximum"
      threshold   = 60
      operator    = "GreaterThanOrEqualToThreshold"
      periods     = 2
      dimensions  = { DBInstanceIdentifier = aws_db_instance.main.identifier }
    }
  }

  ecs_services = toset(["api", "frontend", "worker"])
}

resource "aws_cloudwatch_metric_alarm" "threshold" {
  for_each = local.threshold_alarms

  alarm_name          = "${local.prefix}-${each.key}"
  alarm_description   = each.value.description
  namespace           = each.value.namespace
  metric_name         = each.value.metric
  statistic           = each.value.statistic
  comparison_operator = each.value.operator
  threshold           = each.value.threshold
  period              = 300
  evaluation_periods  = each.value.periods
  dimensions          = each.value.dimensions
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

resource "aws_cloudwatch_metric_alarm" "api_latency_p95" {
  alarm_name          = "${local.prefix}-api-latency-p95"
  alarm_description   = "95th percentile API response time above 2 seconds"
  namespace           = "AWS/ApplicationELB"
  metric_name         = "TargetResponseTime"
  extended_statistic  = "p95"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 2
  period              = 300
  evaluation_periods  = 3
  dimensions          = { LoadBalancer = aws_lb.main.arn_suffix, TargetGroup = aws_lb_target_group.api.arn_suffix }
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

resource "aws_cloudwatch_metric_alarm" "ecs_cpu" {
  for_each = local.ecs_services

  alarm_name          = "${local.prefix}-${each.key}-cpu"
  alarm_description   = "${each.key} CPU above 85% for 15 minutes (the worker is expected to run hot while transcoding)"
  namespace           = "AWS/ECS"
  metric_name         = "CPUUtilization"
  statistic           = "Average"
  comparison_operator = "GreaterThanThreshold"
  threshold           = each.key == "worker" ? 99 : 85
  period              = 300
  evaluation_periods  = 3
  dimensions          = { ClusterName = aws_ecs_cluster.main.name, ServiceName = each.key }
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

resource "aws_cloudwatch_metric_alarm" "ecs_memory" {
  for_each = local.ecs_services

  alarm_name          = "${local.prefix}-${each.key}-memory"
  alarm_description   = "${each.key} memory above 85% for 15 minutes: the task is close to being OOM-killed"
  namespace           = "AWS/ECS"
  metric_name         = "MemoryUtilization"
  statistic           = "Average"
  comparison_operator = "GreaterThanThreshold"
  threshold           = 85
  period              = 300
  evaluation_periods  = 3
  dimensions          = { ClusterName = aws_ecs_cluster.main.name, ServiceName = each.key }
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions
}

# Log-derived alarms use the namespace the metric filters write to.
resource "aws_cloudwatch_metric_alarm" "log_based" {
  for_each = {
    api-errors = {
      description = "10 or more ERROR log lines from the API in 5 minutes"
      metric      = "ApiErrors"
      threshold   = 10
    }
    worker-errors = {
      description = "10 or more ERROR log lines from the worker in 5 minutes"
      metric      = "WorkerErrors"
      threshold   = 10
    }
    jobs-failed = {
      description = "A video processing job failed permanently"
      metric      = "JobsFailed"
      threshold   = 1
    }
  }

  alarm_name          = "${local.prefix}-${each.key}"
  alarm_description   = each.value.description
  namespace           = local.metric_namespace
  metric_name         = each.value.metric
  statistic           = "Sum"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = each.value.threshold
  period              = 300
  evaluation_periods  = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = local.alarm_actions
  ok_actions          = local.alarm_actions

  depends_on = [aws_cloudwatch_log_metric_filter.app]
}

# ---------------------------------------------------------------- dashboard

resource "aws_cloudwatch_dashboard" "main" {
  dashboard_name = local.prefix

  dashboard_body = jsonencode({
    widgets = [
      {
        type = "metric", x = 0, y = 0, width = 12, height = 6
        properties = {
          title  = "Requests and errors"
          region = var.aws_region
          stat   = "Sum"
          period = 300
          metrics = [
            ["AWS/ApplicationELB", "RequestCount", "LoadBalancer", aws_lb.main.arn_suffix],
            [".", "HTTPCode_Target_5XX_Count", ".", ".", "TargetGroup", aws_lb_target_group.api.arn_suffix],
            [".", "HTTPCode_ELB_5XX_Count", "LoadBalancer", aws_lb.main.arn_suffix],
          ]
        }
      },
      {
        type = "metric", x = 12, y = 0, width = 12, height = 6
        properties = {
          title  = "API latency (p95)"
          region = var.aws_region
          stat   = "p95"
          period = 300
          metrics = [
            ["AWS/ApplicationELB", "TargetResponseTime", "LoadBalancer", aws_lb.main.arn_suffix, "TargetGroup", aws_lb_target_group.api.arn_suffix],
          ]
        }
      },
      {
        type = "metric", x = 0, y = 6, width = 12, height = 6
        properties = {
          title  = "ECS CPU %"
          region = var.aws_region
          stat   = "Average"
          period = 300
          metrics = [
            for s in sort(tolist(local.ecs_services)) : ["AWS/ECS", "CPUUtilization", "ClusterName", aws_ecs_cluster.main.name, "ServiceName", s]
          ]
        }
      },
      {
        type = "metric", x = 12, y = 6, width = 12, height = 6
        properties = {
          title  = "ECS memory %"
          region = var.aws_region
          stat   = "Average"
          period = 300
          metrics = [
            for s in sort(tolist(local.ecs_services)) : ["AWS/ECS", "MemoryUtilization", "ClusterName", aws_ecs_cluster.main.name, "ServiceName", s]
          ]
        }
      },
      {
        type = "metric", x = 0, y = 12, width = 12, height = 6
        properties = {
          title  = "Job queue"
          region = var.aws_region
          stat   = "Maximum"
          period = 300
          metrics = [
            ["AWS/SQS", "ApproximateNumberOfMessagesVisible", "QueueName", aws_sqs_queue.jobs.name],
            [".", "ApproximateAgeOfOldestMessage", ".", ".", { yAxis = "right" }],
            [".", "ApproximateNumberOfMessagesVisible", "QueueName", aws_sqs_queue.jobs_dlq.name, { label = "dead-letter queue" }],
          ]
        }
      },
      {
        type = "metric", x = 12, y = 12, width = 12, height = 6
        properties = {
          title  = "Processing jobs"
          region = var.aws_region
          stat   = "Sum"
          period = 300
          metrics = [
            [local.metric_namespace, "JobsSucceeded"],
            [".", "JobsFailed"],
            [".", "WorkerErrors"],
            [".", "ApiErrors"],
          ]
        }
      },
      {
        type = "metric", x = 0, y = 18, width = 12, height = 6
        properties = {
          title  = "Database CPU % and connections"
          region = var.aws_region
          stat   = "Average"
          period = 300
          metrics = [
            ["AWS/RDS", "CPUUtilization", "DBInstanceIdentifier", aws_db_instance.main.identifier],
            [".", "DatabaseConnections", ".", ".", { yAxis = "right" }],
          ]
        }
      },
      {
        type = "metric", x = 12, y = 18, width = 12, height = 6
        properties = {
          title  = "Database free storage and memory (bytes)"
          region = var.aws_region
          stat   = "Minimum"
          period = 300
          metrics = [
            ["AWS/RDS", "FreeStorageSpace", "DBInstanceIdentifier", aws_db_instance.main.identifier],
            [".", "FreeableMemory", ".", "."],
          ]
        }
      },
    ]
  })
}

# ---------------------------------------------------------------- cost guard

resource "aws_budgets_budget" "monthly" {
  name         = "${local.prefix}-monthly"
  budget_type  = "COST"
  limit_amount = tostring(var.monthly_budget_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.alert_email]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.alert_email]
  }
}
