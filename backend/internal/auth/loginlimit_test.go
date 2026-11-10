package auth

import (
	"testing"
	"time"
)

func testLimiter(max int, window time.Duration) (*loginLimiter, *time.Time) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newLoginLimiter(max, window)
	l.now = func() time.Time { return now }
	return l, &now
}

func TestLoginLimiterBlocksAfterMaxFailures(t *testing.T) {
	l, _ := testLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if blocked, _ := l.blocked("a@example.com"); blocked {
			t.Fatalf("blocked after only %d failures", i)
		}
		l.fail("a@example.com")
	}
	blocked, wait := l.blocked("a@example.com")
	if !blocked || wait <= 0 || wait > time.Minute {
		t.Fatalf("want blocked with a wait in (0, 1m], got blocked=%v wait=%v", blocked, wait)
	}
	if other, _ := l.blocked("b@example.com"); other {
		t.Fatal("one address's failures must not block another")
	}
}

func TestLoginLimiterIsCaseAndSpaceInsensitive(t *testing.T) {
	l, _ := testLimiter(1, time.Minute)
	l.fail("Alice@Example.com ")
	if blocked, _ := l.blocked("alice@example.com"); !blocked {
		t.Fatal("different spelling of the same address must share a counter")
	}
}

func TestLoginLimiterWindowExpires(t *testing.T) {
	l, now := testLimiter(1, time.Minute)
	l.fail("a@example.com")
	*now = now.Add(61 * time.Second)
	if blocked, _ := l.blocked("a@example.com"); blocked {
		t.Fatal("still blocked after the window passed")
	}
	l.fail("a@example.com") // a fresh window starts
	if blocked, _ := l.blocked("a@example.com"); !blocked {
		t.Fatal("should be blocked again after a new failure")
	}
}

func TestLoginLimiterSuccessResets(t *testing.T) {
	l, _ := testLimiter(2, time.Minute)
	l.fail("a@example.com")
	l.fail("a@example.com")
	l.success("a@example.com")
	if blocked, _ := l.blocked("a@example.com"); blocked {
		t.Fatal("success must clear the counter")
	}
}

func TestLoginLimiterMemoryIsBounded(t *testing.T) {
	l, _ := testLimiter(1, time.Minute)
	l.maxKeys = 2
	l.fail("a@example.com")
	l.fail("b@example.com")
	l.fail("c@example.com") // over the cap and nothing has expired: not tracked
	if len(l.entries) != 2 {
		t.Fatalf("tracked %d keys, want 2", len(l.entries))
	}
}
