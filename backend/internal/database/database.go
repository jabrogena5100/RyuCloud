// Package database opens PostgreSQL connections and runs the embedded schema migrations.
package database

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

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
