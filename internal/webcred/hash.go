package webcred

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	// FormatBcrypt names the bcrypt credential format Hash produces.
	FormatBcrypt = "bcrypt"
	// FormatSHA256 names the SHA-256 credential format Hash produces.
	FormatSHA256 = "sha256"
)

// Formats lists the credential formats Hash accepts, for a caller that has to
// name them in a usage error.
func Formats() []string { return []string{FormatBcrypt, FormatSHA256} }

// Hash returns the credential line for password in the named format. cost is the
// bcrypt work factor and is ignored by FormatSHA256; zero means
// DefaultBcryptCost.
func Hash(password, format string, cost int) (string, error) {
	switch format {
	case FormatSHA256:
		return HashSHA256(password)
	case FormatBcrypt:
		if cost == 0 {
			cost = DefaultBcryptCost
		}
		return HashBcrypt(password, cost)
	default:
		return "", fmt.Errorf("unknown credential format %q (expected %s)", format, strings.Join(Formats(), " or "))
	}
}

const (
	// DefaultBcryptCost is the work factor used when hashing a password a
	// person typed. bcrypt's own default (10) is on the low side for a secret
	// that guards service actions; 12 keeps verification around a quarter of a
	// second, which the verification cache pays only once per credential.
	DefaultBcryptCost = 12
	// MinBcryptCost is the lowest work factor bcrypt accepts.
	MinBcryptCost = bcrypt.MinCost
	// MaxBcryptCost is the highest work factor bcrypt accepts.
	MaxBcryptCost = bcrypt.MaxCost

	// secretLen is the length in bytes of a generated secret. 256 bits leaves
	// no room for guessing, which is what makes the fast `$sha256$` form safe.
	secretLen = 32
)

// HashBcrypt returns the credential line for a password a person chose.
func HashBcrypt(password string, cost int) (string, error) {
	if password == "" {
		return "", errNoCredentials
	}
	if cost < MinBcryptCost || cost > MaxBcryptCost {
		return "", fmt.Errorf("bcrypt cost %d is out of range (%d-%d)", cost, MinBcryptCost, MaxBcryptCost)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// HashSHA256 returns the salted `$sha256$` credential line for password. It is
// only safe for a high-entropy secret such as the one GenerateSecret returns:
// verification is fast by design, so a guessable password would fall quickly if
// the file leaked.
func HashSHA256(password string) (string, error) {
	if password == "" {
		return "", errNoCredentials
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read random salt: %w", err)
	}
	digest := saltedDigest(salt, password)
	return PrefixSHA256 + encode(salt) + fieldSeparator + encode(digest[:]), nil
}

// GenerateSecret returns a new random secret, used both for a generated
// dashboard credential and for the daemon's runtime token.
func GenerateSecret() (string, error) {
	secret := make([]byte, secretLen)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("read random secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}

// encode renders a credential field. Raw (unpadded) standard base64 has no `$`,
// so it cannot collide with the field separator.
func encode(b []byte) string { return base64.RawStdEncoding.EncodeToString(b) }
