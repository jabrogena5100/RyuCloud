package auth

import (
	"strings"
	"sync"
	"time"
)

// loginLimiter throttles failed sign-in attempts per e-mail address so that online password
// guessing is bounded. It is deliberately simple:
//
//   - It is keyed by the normalized e-mail, not the client IP. Behind a load balancer the
//     socket address is the balancer's, and trusting X-Forwarded-For without knowing the proxy
//     chain would let an attacker pick their own key.
//   - State is in memory, so each API process counts separately (one task today). Behind
//     several tasks the effective limit is max × tasks, which still bounds guessing.
//   - A successful sign-in clears the counter. A limited address is refused before any
//     password hashing happens, so a flood of attempts cannot also burn CPU on Argon2.
//
// The cost is that someone can lock a known address out for one window by failing on purpose;
// the window is short and the lockout does not touch sessions that already exist.
type loginLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	entries map[string]*loginAttempts
}

type loginAttempts struct {
	failures int
	first    time.Time // start of the current window
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, maxKeys: 50000, now: time.Now, entries: map[string]*loginAttempts{}}
}

func limiterKey(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// blocked reports whether email has used up its failures, and if so how long until it may retry.
func (l *loginLimiter) blocked(email string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := limiterKey(email)
	e, ok := l.entries[key]
	if !ok {
		return false, 0
	}
	remaining := e.first.Add(l.window).Sub(l.now())
	if remaining <= 0 {
		delete(l.entries, key)
		return false, 0
	}
	return e.failures >= l.max, remaining
}

// fail records one failed attempt.
func (l *loginLimiter) fail(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := limiterKey(email)
	now := l.now()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.first) >= l.window {
		if !ok && len(l.entries) >= l.maxKeys {
			l.evictExpired(now)
			if len(l.entries) >= l.maxKeys {
				return // memory bound: under a flood of distinct addresses, stop tracking new ones
			}
		}
		e = &loginAttempts{first: now}
		l.entries[key] = e
	}
	e.failures++
}

// success forgets the address.
func (l *loginLimiter) success(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, limiterKey(email))
}

func (l *loginLimiter) evictExpired(now time.Time) {
	for k, e := range l.entries {
		if now.Sub(e.first) >= l.window {
			delete(l.entries, k)
		}
	}
}
