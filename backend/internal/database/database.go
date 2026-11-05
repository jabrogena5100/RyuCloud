// Package database opens PostgreSQL connections and runs the embedded schema migrations.
package database

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver used by goose
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Open creates a connection pool. Connections are established lazily.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return pool, nil
}

// Migrate runs a goose command against the migrations embedded in the binary:
// "up" (apply all), "down" (roll back one), "reset" (roll back all) or "status".
func Migrate(ctx context.Context, url, command string) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrationFiles)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}

	switch command {
	case "up":
		return goose.UpContext(ctx, db, "migrations")
	case "down":
		return goose.DownContext(ctx, db, "migrations")
	case "reset":
		return goose.ResetContext(ctx, db, "migrations")
	case "status":
		return goose.StatusContext(ctx, db, "migrations")
	default:
		return fmt.Errorf("unknown migrate command %q (use up, down, reset or status)", command)
	}
}

// IsUniqueViolation reports whether err is a unique-constraint violation on the named constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

var migrationFilePattern = regexp.MustCompile(`^(\d+)_.+\.sql$`)

// ExpectedSchemaVersion is the highest migration version embedded in this binary: the schema the
// code was written against. It reads only the embedded files, never the database.
func ExpectedSchemaVersion() (int64, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}
	var highest int64
	for _, entry := range entries {
		match := migrationFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q: %w", entry.Name(), err)
		}
		highest = max(highest, version)
	}
	if highest == 0 {
		return 0, errors.New("no embedded migrations found")
	}
	return highest, nil
}

// appliedVersionSQL takes the newest goose row per version (a rollback is recorded as a later row
// with is_applied=false) and returns the highest version that is currently applied.
const appliedVersionSQL = `
SELECT COALESCE(MAX(version_id), 0)
FROM (
    SELECT DISTINCT ON (version_id) version_id, is_applied
    FROM goose_db_version
    ORDER BY version_id, id DESC
) latest
WHERE is_applied`

// querier is satisfied by *pgxpool.Pool.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// AppliedSchemaVersion returns the highest migration version applied to the database, or 0 when
// no migration has run (including when goose's bookkeeping table does not exist yet).
func AppliedSchemaVersion(ctx context.Context, db querier) (int64, error) {
	var version int64
	err := db.QueryRow(ctx, appliedVersionSQL).Scan(&version)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read applied schema version: %w", err)
	}
	return version, nil
}
