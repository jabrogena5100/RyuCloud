package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argonParams are Argon2id cost settings. The defaults are the OWASP Password
// Storage Cheat Sheet recommendation: 19 MiB of memory, 2 passes, 1 lane.
type argonParams struct {
	memoryKiB uint32
	passes    uint32
	lanes     uint8
	keyLen    uint32
}

var defaultParams = argonParams{memoryKiB: 19 * 1024, passes: 2, lanes: 1, keyLen: 32}

const saltLen = 16

var (
	b64            = base64.RawStdEncoding
	errInvalidHash = errors.New("invalid argon2id hash")
)

// dummyHash is verified against when an email is unknown, so failed logins take
// similar time whether or not the account exists.
var dummyHash = sync.OnceValue(func() string {
	hash, _ := hashWithParams("streamforge-timing-equalizer", defaultParams)
	return hash
})

// HashPassword returns an Argon2id hash in the standard PHC string format:
// $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<key>
func HashPassword(password string) (string, error) {
	return hashWithParams(password, defaultParams)
}

func hashWithParams(password string, p argonParams) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.passes, p.memoryKiB, p.lanes, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memoryKiB, p.passes, p.lanes, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the encoded hash, and whether
// the hash uses outdated parameters and should be replaced.
func VerifyPassword(encoded, password string) (ok, needsRehash bool) {
	p, salt, key, err := decodeHash(encoded)
	if err != nil {
		return false, false
	}
	candidate := argon2.IDKey([]byte(password), salt, p.passes, p.memoryKiB, p.lanes, p.keyLen)
	if subtle.ConstantTimeCompare(key, candidate) != 1 {
		return false, false
	}
	return true, p != defaultParams
}

func decodeHash(encoded string) (p argonParams, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, errInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, errInvalidHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.passes, &p.lanes); err != nil {
		return p, nil, nil, errInvalidHash
	}
	if p.memoryKiB == 0 || p.passes == 0 || p.lanes == 0 {
		return p, nil, nil, errInvalidHash
	}
	if salt, err = b64.DecodeString(parts[4]); err != nil {
		return p, nil, nil, errInvalidHash
	}
	if key, err = b64.DecodeString(parts[5]); err != nil || len(key) == 0 {
		return p, nil, nil, errInvalidHash
	}
	p.keyLen = uint32(len(key))
	return p, salt, key, nil
}
