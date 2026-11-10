module github.com/jabrogena5100/RyuCloud/backend

go 1.25.0

// Dependency policy: the toolchain is Go 1.25 (see ARG GO_VERSION in Dockerfile; GOTOOLCHAIN=local, so Go
// never downloads another one). A module whose own "go" line is newer than the toolchain makes `go mod tidy`
// fail, and tidy resolves any module that is not pinned here to its LATEST release. So every directly imported
// module is pinned below, at the newest release whose whole dependency closure needs no more than Go 1.24
// (checked with scripts/check-go-deps.py). Do not run `go get -u`; to upgrade, `go get module@version`
// and re-run the checker.
require (
	github.com/alicebob/miniredis/v2 v2.40.0 // tests only
	github.com/aws/aws-sdk-go-v2 v1.47.7
	github.com/aws/aws-sdk-go-v2/config v1.33.14
	github.com/aws/aws-sdk-go-v2/credentials v1.20.14
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.7
	github.com/aws/smithy-go v1.28.8
	github.com/jackc/pgx/v5 v5.9.2
	github.com/pressly/goose/v3 v3.24.3
	github.com/redis/go-redis/v9 v9.22.0 // v9.23.0 and later need Go 1.26
	golang.org/x/crypto v0.39.0
)

require github.com/aws/aws-sdk-go-v2/service/sqs v1.52.10

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.26 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.6 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.12.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.7 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/sethvargo/go-retry v0.3.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
