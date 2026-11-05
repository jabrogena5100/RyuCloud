package server_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
	"github.com/jabrogena5100/RyuCloud/backend/internal/database"
)

// All database-backed tests live in this one package so they never run in
// parallel against the shared test database.
var (
	testPool   *pgxpool.Pool
	testConfig config.Config
)

// TestMain prepares a dedicated database named by TEST_DATABASE_URL (created if
// missing), wipes it, and builds the schema by running the migrations up, all
// the way down, and up again, so every run also exercises the down migrations.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, `TEST_DATABASE_URL is not set; see README "Running tests"`)
		return 1
	}
	if url == os.Getenv("DATABASE_URL") {
		fmt.Fprintln(os.Stderr, "TEST_DATABASE_URL must differ from DATABASE_URL (tests wipe the database)")
		return 1
	}

	ctx := context.Background()
	if err := prepareDatabase(ctx, url); err != nil {
		fmt.Fprintln(os.Stderr, "prepare test database:", err)
		return 1
	}
	for _, command := range []string{"up", "reset", "up"} {
		if err := database.Migrate(ctx, url, command); err != nil {
			fmt.Fprintf(os.Stderr, "migrate %s: %v\n", command, err)
			return 1
		}
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return 1
	}
	defer pool.Close()

	testPool = pool
	testConfig = config.Config{
		Environment:       "test",
		DatabaseURL:       url,
		SessionCookieName: cookieName,
		SessionTTL:        time.Hour,
		CookieSecure:      false, // httptest serves plain HTTP
		TrustedOrigins:    []string{trustedOrigin},
		EnableAPIDocs:     true,
		QueueBackend:      "memory",
		Upload:            config.UploadConfig{MaxBytes: 100 << 20, URLTTL: 15 * time.Minute},
	}
	return m.Run()
}

var dbNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// prepareDatabase creates the test database if needed and empties its public schema.
func prepareDatabase(ctx context.Context, url string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return err
	}
	name := cfg.Database
	if !dbNamePattern.MatchString(name) {
		return fmt.Errorf("refusing to use unexpected database name %q", name)
	}

	adminCfg := cfg.Copy()
	adminCfg.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		return err
	}
	defer admin.Close(ctx)

	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, name)); err != nil {
			return err
		}
	}

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	for _, stmt := range []string{`DROP SCHEMA IF EXISTS public CASCADE`, `CREATE SCHEMA public`} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
