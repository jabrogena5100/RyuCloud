package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordFormatAndVerify(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	if ok, rehash := VerifyPassword(hash, "correct-horse-battery"); !ok || rehash {
		t.Fatalf("VerifyPassword(correct) = %v, %v; want true, false", ok, rehash)
	}
	if ok, _ := VerifyPassword(hash, "wrong-password"); ok {
		t.Fatal("wrong password verified")
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same-password")
	b, _ := HashPassword("same-password")
	if a == b {
		t.Fatal("two hashes of the same password should differ")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, hash := range []string{
		"",
		"plain-text",
		"$argon2i$v=19$m=1024,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=0,t=0,p=0$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=1024,t=1,p=1$!!!$a2V5a2V5",
	} {
		if ok, _ := VerifyPassword(hash, "anything"); ok {
			t.Errorf("malformed hash %q verified", hash)
		}
	}
}

func TestOutdatedParamsNeedRehash(t *testing.T) {
	weak := argonParams{memoryKiB: 8 * 1024, passes: 1, lanes: 1, keyLen: 32}
	hash, err := hashWithParams("pw-123456", weak)
	if err != nil {
		t.Fatal(err)
	}
	if ok, rehash := VerifyPassword(hash, "pw-123456"); !ok || !rehash {
		t.Fatalf("VerifyPassword = %v, %v; want true, true", ok, rehash)
	}
}

func TestSessionTokens(t *testing.T) {
	a, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newSessionToken()
	if a == b || len(a) != 43 {
		t.Fatalf("tokens should be unique 43-char strings, got %q and %q", a, b)
	}
	h := hashSessionToken(a)
	if len(h) != 64 || h == a || hashSessionToken(a) != h {
		t.Fatalf("unexpected token hash %q", h)
	}
}
