# AWS cost estimate

**These are estimates, not measurements, and nothing has been deployed.** They use public on-demand list prices for
`us-east-1` as best known when this was written (730 hours/month). Prices change and differ by region: confirm with
the [AWS Pricing Calculator](https://calculator.aws/) and the Billing console before applying.

## Fixed monthly cost with the default settings (always on, no traffic)

| Item | Basis | ≈ USD/month |
| --- | --- | --- |
| Application Load Balancer | $0.0225/h + about 1 LCU at light load | 22 |
| Fargate: API (0.25 vCPU, 0.5 GB) | $0.04048/vCPU-h + $0.004445/GB-h | 9 |
| Fargate: frontend (0.25 vCPU, 0.5 GB) | same | 9 |
| Fargate: worker (1 vCPU, 2 GB) | on-demand ≈ 36; Spot is typically ~70% less and varies | 11 – 36 |
| Public IPv4 addresses | $0.005/h each: 3 tasks + 2 ALB addresses | 18 |
| RDS `db.t4g.micro`, single-AZ | $0.016/h | 12 |
| RDS storage, 20 GiB gp3 | $0.115/GiB | 2.3 |
| Secrets Manager (2 secrets) | $0.40 each | 0.8 |
| Route 53 hosted zone | $0.50 (you may already have it) | 0.5 |
| CloudWatch: 20 alarms, 4 custom metrics, 1 dashboard | $0.10 / $0.30 / first 3 free | 3.2 |
| CloudWatch Logs ingestion + storage | $0.50/GB ingested; light use | 1 – 3 |
| ECR image storage | $0.10/GB-month; about 3 repos × a few hundred MB | 1 |
| **Total (idle)** | | **≈ 80 – 105** |

The default `monthly_budget_usd` is 150, so the 80% alert (≈ $120) fires before the baseline is exceeded.

## Usage-based costs (scale with how much you use it)

| Item | Price | Note |
| --- | --- | --- |
| S3 storage | ≈ $0.023/GB-month | Originals + 2 renditions of each video, roughly 1.5–2.5× the source size |
| S3 requests | PUT $0.005 / 1,000; GET $0.0004 / 1,000 | HLS segments mean many small objects per video |
| CloudFront egress | first 1 TB/month free (always-free tier), then ≈ $0.085/GB in `PriceClass_100` | Video delivery is the cost that can grow quickly |
| CloudFront requests | ≈ $0.0075–0.01 per 10,000 HTTPS requests after the free 10 M | |
| SQS | first 1 M requests/month free | Negligible here |
| Data transfer out of AWS (ALB/API) | $0.09/GB after 100 GB free | API responses are small |

## Cost levers

| Lever | Saves | Cost of the lever |
| --- | --- | --- |
| Pause when not demoing: set services to 0 tasks (`deploy_services = false` or `aws ecs update-service --desired-count 0`) | ≈ $30 – 55 (Fargate + task IPv4) | Site is down; ALB and RDS keep billing |
| Stop RDS (`aws rds stop-db-instance`) | ≈ $12 compute | AWS auto-restarts it after 7 days; storage still billed |
| `terraform destroy` between demos | everything | Re-apply takes ~20–30 min; set `protect_data = false` first, which deletes data |
| Graviton (ARM64) tasks | ≈ 20% of Fargate | Requires building `linux/arm64` images (QEMU or ARM runners) |
| Keep `worker_use_spot = true` | ≈ 25 | Interrupted jobs restart |
| Expire originals (`source_retention_days`) | S3 storage | Original uploads are deleted, so videos cannot be re-processed |

## Upgrades you may want, and what they add (roughly)

| Upgrade | + USD/month |
| --- | --- |
| NAT gateway (tasks in private subnets, removes task public IPs) | +33 per AZ, −11 for the IPs |
| RDS Multi-AZ | +12 |
| AWS WAF on the ALB | ≈ +6 plus per-request charges |
| Container Insights | per-metric charges; can exceed $10 |
| Second API/frontend task for availability | +9 each |

## Guardrails built in

- `aws_budgets_budget` emails you at 80% actual and 100% forecast.
- Storage autoscaling for RDS is capped (`db_max_allocated_storage_gib`, default 50).
- Task sizes are explicit, `desired_count` is bounded by variable validation, log retention is 14 days, and untagged
  ECR repositories keep only the 15 newest images.
- Create a separate **AWS Budgets / Cost Anomaly Detection** alert in the console if you want protection beyond this
  stack (e.g., an accidentally launched resource elsewhere in the account).
