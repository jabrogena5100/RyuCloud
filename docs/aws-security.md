# AWS security model

## Principles applied

| Area | What is configured | Where |
| --- | --- | --- |
| No public data stores | S3 Block Public Access on; RDS in isolated subnets (no internet route), `publicly_accessible = false` | `s3.tf`, `rds.tf`, `network.tf` |
| Private media | Bucket policy lets only this CloudFront distribution read `videos/*` (OAC + `AWS:SourceArn`). `uploads/*` originals are not readable by the CDN. Every CDN request needs a valid signature | `s3.tf`, `cloudfront.tf` |
| Encryption in transit | ALB redirects HTTP→HTTPS (TLS 1.3/1.2 policy); CloudFront `https-only`, TLS 1.2+; S3 and SQS policies deny non-TLS; RDS `rds.force_ssl=1` and `sslmode=require` | `alb.tf`, `s3.tf`, `sqs.tf`, `rds.tf` |
| Encryption at rest | S3 SSE-S3, SQS SSE, RDS storage encryption, Secrets Manager | various |
| Least-privilege IAM | Separate execution, API and worker roles; the worker can read only `uploads/*` and write only `videos/*`; the API cannot receive/delete queue messages; no `*` actions; the frontend has no role | `iam.tf` |
| Confused-deputy protection | Task roles trust `ecs-tasks.amazonaws.com` only for this account | `iam.tf` |
| Network isolation | Tasks accept traffic only from the ALB security group; the database accepts only the task security group; egress is limited to HTTPS and PostgreSQL | `network.tf` |
| Secrets | DB password generated and stored by RDS (never in Terraform state or the repo); the CloudFront private key is stored by hand in Secrets Manager; both are injected as env at task start | `rds.tf`, `secrets.tf`, `ecs.tf` |
| Supply chain | ECR immutable tags + scan on push; images run as non-root; API root filesystem is read-only; distroless API image | `ecr.tf`, `ecs.tf`, `backend/Dockerfile` |
| CI/CD credentials | GitHub Actions assumes a role through OIDC (no stored keys); trust is limited to one repository's `production` environment; the role can only push to the three ECR repos and update the three services | `github_oidc.tf` |
| Auditability | CloudWatch Logs for all services and RDS PostgreSQL logs; enable CloudTrail separately (an account-level setting, outside this stack) | `monitoring.tf` |

## Threat notes

- **Guessing media URLs does not work.** Without signed cookies or a signed URL, CloudFront returns 403; going to
  S3 directly returns 403 (no public access, policy allows only the distribution).
- **Cookie scope.** A playback cookie authorises one video's prefix (`/videos/{id}/*`) for two hours. It is `HttpOnly`
  (not readable by page scripts) and `Secure`. Someone who steals the cookie jar can replay it until it expires.
  Revocation before expiry is not possible with CloudFront signed cookies; keep the TTL short for sensitive content.
- **Private/unlisted videos.** Cookies are only issued after the API's own visibility check, but a viewer who
  legitimately received cookies can share the media URLs and cookie values within the TTL.
- **Task public IPs.** Tasks have public IPs only so they can reach AWS APIs without a NAT gateway. The security group
  has no inbound rule except from the ALB, so they are not reachable directly. A NAT/VPC-endpoint design removes
  the public IPs entirely (see [aws-costs.md](aws-costs.md)).
- **SHA-1.** CloudFront defines signatures as RSA-SHA1; this is a protocol constraint, not a choice. The key is 2048-bit
  RSA minimum (enforced at startup).
- **RDS certificate verification.** `sslmode=require` encrypts but does not verify the server certificate. For
  `verify-full`, bundle the RDS CA into the image and set `DB_SSLMODE=verify-full`.

## Operational hygiene

1. Never commit `terraform.tfvars`, `backend.hcl`, `*.pem`, or state. `.gitignore` excludes them.
2. Keep the CloudFront private key outside the repository; after storing it in Secrets Manager, remove the local
   copy or keep it in a password manager.
3. Rotate the signing key: create a second public key and key group entry, store the new private key, deploy, then
   remove the old public key.
4. Use an IAM Identity Center / non-root user with MFA for Terraform. The Terraform principal needs broad permissions
   (it creates IAM roles); the CI role does not.
5. Turn on the GitHub `production` environment's **required reviewers** so every deployment needs approval.
6. Pin third-party GitHub Actions to commit SHAs if you need stronger supply-chain guarantees (this repo pins major
   versions for readability).
