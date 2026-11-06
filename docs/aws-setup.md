# AWS account setup (before the first Terraform run)

Nothing on this page creates billable resources except where marked. Decisions and blockers are in
[phase5-plan.md](phase5-plan.md); the deployment steps are in [aws-runbook.md](aws-runbook.md).
Order: account hygiene → AWS CLI → keyless login → domain → the two config files.

## 1. Account hygiene (console, once)

- **MFA on the root user** (done). Do not use root for daily work and do not create root access keys.
- Billing → **Billing preferences**: enable free-tier usage alerts and billing alert emails.
- Billing → **Budgets**: create an account-level budget by hand (the first two are free), e.g. alerts at $20 and $50 actual. The budget in
  Terraform is destroyed with the stack, so it cannot be your only protection.
- Do **not** enable AWS Organizations or IAM Identity Center. On a Free-plan account, creating or joining an Organization upgrades the
  account and ends credit eligibility; Identity Center is currently unavailable on this account (phase5-plan B1).

## 2. Install AWS CLI v2 (VM; done, 2.39.5)

```bash
sudo apt update && sudo apt install -y unzip
mkdir -p ~/awscli-install && cd ~/awscli-install
curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o awscliv2.zip
unzip -q awscliv2.zip
sudo ./aws/install
aws --version        # aws-cli/2.x
```

## 3. Keyless login without Identity Center: IAM user + `aws login`

Free and available on every plan. No access keys are ever created; the CLI holds short-lived credentials that it refreshes.

Console, once:
1. **IAM → User groups → Create** `ryucloud-admins`; attach **AdministratorAccess** and **SignInLocalDevelopmentAccess**.
   (Terraform creates IAM roles, so a narrow policy does not work yet. Narrowing is a later hardening task.)
2. **IAM → Users → Create** `ryucloud-admin`: console access, member of `ryucloud-admins`, then add an **MFA device** to it.
   **Do not create an access key.** Check: IAM → the user → Security credentials shows no access keys.
3. Sign out of root; sign in as `ryucloud-admin` (use the account's sign-in URL shown on the IAM dashboard).

On the VM:
```bash
aws login --profile ryucloud            # add --remote if the VM has no browser, then paste the code it displays
export AWS_PROFILE=ryucloud
aws sts get-caller-identity             # ARN ends in user/ryucloud-admin
aws configure export-credentials --format env-no-export | cut -d= -f1    # prints variable NAMES only
aws logout                              # when you are done for the day
```
Re-run `aws login` when the session expires. Static keys in `~/.aws/credentials` would override the login session; there should be none.
`scripts/aws-terraform.sh` turns the login into temporary environment variables for the Terraform container for that one command only.

(Later, optional: after an upgrade to the Paid plan you may reconsider IAM Identity Center. It is not needed for anything in this repository.)

## 4. Region: `us-west-2`

Decided. Use the **same** region in `terraform.tfvars` (`aws_region`; it is also the variable's default), `backend.hcl` (`region`), the CLI profile
(`aws configure set region us-west-2 --profile ryucloud`) and the GitHub `AWS_REGION` variable. The CloudFront certificate is created in
`us-east-1` automatically. Do not change region after the first apply (everything would be re-created). Create the state bucket in `us-west-2`
with `--create-bucket-configuration LocationConstraint=us-west-2`.

## 5. Domain (not purchased yet; required before any apply)

The app needs a real domain: signed CloudFront cookies cannot work on `*.cloudfront.net` or the load balancer's own name. You need a **public
Route 53 hosted zone that contains the app name**.

| Situation | Approach | Cost |
| --- | --- | --- |
| No domain yet (your case) | Buy one. Registering in Route 53 keeps the zone and registration together; other registrars work if you delegate the name servers. Use a subdomain such as `ryucloud.<yourdomain>` for the app. | A yearly fee that varies by TLD (check the price at checkout) + $0.50/month for the zone |
| You own a domain at another registrar | **Delegate a subdomain**: create a hosted zone for `ryucloud.example.com` in Route 53, add its four NS records at your registrar. | Zone $0.50/month |

Buying a domain and creating the hosted zone are charged actions and are **not done**. After delegation, check:
```bash
dig +short NS ryucloud.example.com        # must list the four awsdns-* servers of your hosted zone
```
Domain registration may also be restricted on the Free plan (unverified); see phase5-plan §5.

## 6. The two config files (git-ignored; templates are committed)

```bash
cd ~/projects/RyuCloud/infra/terraform
cp backend.hcl.example backend.hcl
cp terraform.tfvars.example terraform.tfvars
vi backend.hcl
vi terraform.tfvars
```
Replace every `REPLACE...` value; the files explain each one:
- `backend.hcl`: `bucket` (globally unique, e.g. `ryucloud-tfstate-<12-digit-account-id>`; the bucket does not exist yet), `region = "us-west-2"`.
- `terraform.tfvars`: `domain_name`, `hosted_zone_id` (the part after `/hostedzone/`), `alert_email`; `deploy_services = false`; `protect_data = false` for a
  temporary environment (`true` for a long-lived one; choose before the first apply).

Neither file is ever committed (`.gitignore` covers them). Leave `github_repository` unset until the GitHub `production` environment is restricted and verified.
