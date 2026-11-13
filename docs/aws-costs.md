# AWS cost estimate

**These are estimates, not measurements, and nothing has been deployed.** They use public on-demand list prices
as best known when this was written (730 hours/month), taken from `us-east-1`. **The chosen region is `us-west-2`.** To my
knowledge Oregon list prices for ALB, RDS `db.t4g.micro`, gp3, public IPv4, S3, Secrets Manager and CloudWatch match
`us-east-1`, and Fargate in Oregon is priced the same as Virginia (Northern California is about 15% higher), but I could
not confirm Oregon's figures from the public pages, so treat every number as **unverified for us-west-2**. Prices change:
check the [AWS Pricing Calculator](https://calculator.aws/), or the Price List API once you are logged in
(`aws pricing get-products --region us-east-1 --service-code AmazonECS ...`, read-only), before applying.

**Correction (Phase 5):** re-adding the rows below gives about $90 to $117, not the "80 to 105" first published. The idle
figure with no tasks running is about $50 (ALB 22 + 2 ALB addresses 7 + RDS 14 + secrets, DNS, alarms, logs, ECR about 6).

Session-based cost estimates (8 h, 24 h, 7 days) and persistent items are in [temporary-deployment.md](temporary-deployment.md) §7.

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
| **Total, all services running, no traffic** | rows above (low: Spot worker, 1 GB logs; high: on-demand worker, 3 GB logs) | **≈ 90 – 117** |
| **Total, infrastructure only (`deploy_services = false`, zero tasks)** | ALB 22, ALB IPv4 7, RDS 14.3, secrets 0.8, DNS 0.5, alarms 3.2, logs 0–1, ECR 1 | **≈ 49 – 50** |

The default `monthly_budget_usd` is 150; `terraform.tfvars.example` suggests 60 for a temporary environment. That budget is created by this
stack and is destroyed with it, so also create an account-level budget by hand.

## On-demand vs always-running

Hourly cost of the whole stack up (list prices, unverified for us-west-2): ALB 0.0225 + about 0.008 LCU = 0.0305; five public IPv4
addresses 0.025; RDS 0.016; API and frontend tasks 2 × 0.0123 = 0.0247; worker 0.049 on-demand or about 0.015 on Spot. Sum:
**≈ $0.11 – 0.15 per hour**. Monthly items (storage, secrets, alarms) add under $0.01 per hour.

| | Temporary sessions (`protect_data = false`, destroy afterwards) | Always running (`protect_data = true`) |
| --- | --- | --- |
| Cost | 4 h session ≈ $0.45 – 0.60; 8 sessions of 4 h a month ≈ $3.50 – 4.60, plus about $0.50/month for the Route 53 zone and the yearly domain fee | ≈ $90 – 117 per month, plus CloudFront and S3 usage |
| Setup time per session | ≈ 20 – 30 min to apply (RDS, CloudFront, ACM); ≈ 10 – 20 min to destroy | None |
| Data | Database and videos are deleted (no snapshot). Re-seed test data each time. Restoring from a snapshot is not implemented. | Kept; final snapshot on destroy |
| Public availability | Only while up; a domain name that points nowhere in between | Continuous |
| What it teaches | Repeatable provisioning, teardown, cost discipline | Operations: alarms, backups, deploys, patching |
| Main risks | Forgetting to destroy (≈ $2.70 – 3.50 per day if left running); the first apply failing half-way on a Free-plan restriction | Cost creep; single-AZ database, one task per service |

Middle path: keeping the infrastructure up with zero tasks (≈ $50 per month) rarely makes sense; destroy instead.

**Can this fit $10 – 25 per month?** Not as designed. Even with zero tasks the stack costs about $50. The monthly total can fall into that
range only for temporary use (a few sessions per month), or with a different, cheaper architecture (for example one small EC2 instance
running Docker Compose, no ALB, no RDS), which would be a separate design decision and has not been costed or built.

**Credits.** A new Free-plan account has credits (AWS documents up to $200, valid for a limited period). Temporary sessions fit comfortably inside
that; an always-running stack would use it up in roughly two months. Verify the balance in Billing before relying on it.

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
