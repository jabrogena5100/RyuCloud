package server_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jabrogena5100/RyuCloud/backend/internal/database"
)

// Schema-level guarantees, checked directly against PostgreSQL.

func insertUser(t *testing.T, email string) string {
	t.Helper()
	var id string
	err := testPool.QueryRow(context.Background(),
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, 'not-a-real-hash', 'DB') RETURNING id`,
		email).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertChannel(t *testing.T, ownerID string) string {
	t.Helper()
	var id string
	err := testPool.QueryRow(context.Background(),
		`INSERT INTO channels (owner_id, handle, name) VALUES ($1, 'db-tv', 'DB TV') RETURNING id`,
		ownerID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestUserDefaults(t *testing.T) {
	truncateTables(t)
	id := insertUser(t, "db@example.com")
	var active bool
	if err := testPool.QueryRow(context.Background(), `SELECT is_active FROM users WHERE id = $1`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("is_active should default to true")
	}
}

func TestEmailUniquenessIsEnforcedByDatabase(t *testing.T) {
	truncateTables(t)
	insertUser(t, "db@example.com")
	_, err := testPool.Exec(context.Background(),
		`INSERT INTO users (email, password_hash, display_name) VALUES ('db@example.com', 'x', 'DB')`)
	if !database.IsUniqueViolation(err, "uq_users_email") {
		t.Fatalf("expected a unique violation, got %v", err)
	}
}

func TestEmailMustBeStoredLowercase(t *testing.T) {
	truncateTables(t)
	_, err := testPool.Exec(context.Background(),
		`INSERT INTO users (email, password_hash, display_name) VALUES ('Mixed@Example.com', 'x', 'DB')`)
	if pgCode(err) != "23514" { // check_violation
		t.Fatalf("expected a check violation, got %v", err)
	}
}

func TestVideoStatusCheckConstraint(t *testing.T) {
	truncateTables(t)
	channelID := insertChannel(t, insertUser(t, "db@example.com"))
	_, err := testPool.Exec(context.Background(),
		`INSERT INTO videos (channel_id, title, status) VALUES ($1, 'Bad', 'bogus')`, channelID)
	if pgCode(err) != "23514" {
		t.Fatalf("expected a check violation, got %v", err)
	}
}

func TestDeletingUserCascadesToChannelsAndVideos(t *testing.T) {
	truncateTables(t)
	ctx := context.Background()
	userID := insertUser(t, "db@example.com")
	execSQL(t, `INSERT INTO videos (channel_id, title) VALUES ($1, 'Doomed')`, insertChannel(t, userID))

	execSQL(t, `DELETE FROM users WHERE id = $1`, userID)
	var channels, videos int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM channels), (SELECT count(*) FROM videos)`).Scan(&channels, &videos); err != nil {
		t.Fatal(err)
	}
	if channels != 0 || videos != 0 {
		t.Fatalf("expected cascade delete, found %d channels and %d videos", channels, videos)
	}
}
