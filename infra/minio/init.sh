#!/bin/sh
# One-shot bucket initialisation for local development (run by the minio-init
# service in docker-compose.yml). Safe to run repeatedly.
#
#   - creates the private media bucket
#   - creates a least-privilege application user whose policy only covers that bucket
#     (the API never uses the MinIO root credentials)
#
# CORS for browser uploads is not configured here: MinIO does not implement the
# S3 bucket-CORS API and takes allowed origins from MINIO_API_CORS_ALLOW_ORIGIN
# on the server instead (see docker-compose.yml). AWS S3 needs a bucket CORS
# rule instead; see docs/uploads.md.
set -eu

: "${MINIO_ROOT_USER:?}" "${MINIO_ROOT_PASSWORD:?}" "${S3_BUCKET:?}" "${S3_ACCESS_KEY_ID:?}" "${S3_SECRET_ACCESS_KEY:?}"

attempts=0
until mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 30 ]; then
    echo "MinIO did not become ready in time" >&2
    exit 1
  fi
  sleep 1
done

mc mb --ignore-existing "local/$S3_BUCKET"
mc anonymous set none "local/$S3_BUCKET" # no public access

cat > /tmp/ryucloud-app-policy.json <<POLICY
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload"],
      "Resource": ["arn:aws:s3:::${S3_BUCKET}/*"]
    },
    {
      "Effect": "Allow",
      "Action": ["s3:ListBucket"],
      "Resource": ["arn:aws:s3:::${S3_BUCKET}"]
    }
  ]
}
POLICY

mc admin policy create local ryucloud-app /tmp/ryucloud-app-policy.json
mc admin user add local "$S3_ACCESS_KEY_ID" "$S3_SECRET_ACCESS_KEY"
if ! mc admin user info local "$S3_ACCESS_KEY_ID" | grep -q "ryucloud-app"; then
  mc admin policy attach local ryucloud-app --user "$S3_ACCESS_KEY_ID"
fi

echo "Bucket '$S3_BUCKET' is ready and private; application user '$S3_ACCESS_KEY_ID' is configured."
