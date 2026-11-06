# Job queue. A message carries only a job ID; PostgreSQL is the source of truth, so a lost or
# duplicated message cannot corrupt a job. Messages that are received but never acknowledged
# (a crashing worker) are retried, then moved to the dead-letter queue, which raises an alarm.

resource "aws_sqs_queue" "jobs_dlq" {
  name                      = "${local.prefix}-jobs-dlq"
  message_retention_seconds = 1209600 # 14 days: time to investigate
  sqs_managed_sse_enabled   = true
}

resource "aws_sqs_queue" "jobs" {
  name = "${local.prefix}-jobs"

  # Longer than WORKER_JOB_TIMEOUT_SECONDS (1800) so a running job is not handed to a second worker.
  visibility_timeout_seconds = 1860
  message_retention_seconds  = 345600 # 4 days
  receive_wait_time_seconds  = 20     # long polling
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.jobs_dlq.arn
    maxReceiveCount     = var.sqs_max_receive_count
  })
}

resource "aws_sqs_queue_redrive_allow_policy" "jobs_dlq" {
  queue_url = aws_sqs_queue.jobs_dlq.id

  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.jobs.arn]
  })
}

data "aws_iam_policy_document" "sqs_tls_only" {
  for_each = {
    jobs = aws_sqs_queue.jobs
    dlq  = aws_sqs_queue.jobs_dlq
  }

  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["sqs:*"]
    resources = [each.value.arn]

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
}

resource "aws_sqs_queue_policy" "tls_only" {
  for_each = {
    jobs = aws_sqs_queue.jobs
    dlq  = aws_sqs_queue.jobs_dlq
  }

  queue_url = each.value.id
  policy    = data.aws_iam_policy_document.sqs_tls_only[each.key].json
}
