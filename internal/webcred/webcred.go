// Package webcred parses and verifies the dashboard credentials configured in
// `web.password` / `web.password_file` (and their guest counterparts).
//
// A credential source holds one credential per line, in cleartext or hashed:
//
//	$2b$12$K3JqR7uH...          # bcrypt, for a password a person types
//	$sha256$c2FsdA$9b74c9bd...  # salted SHA-256, for a generated secret
//	s3cret-en-claro             # cleartext
//
// Hashing keeps the dashboard password out of the filesystem in readable form
// without any decryption key to provision: unlike an encrypted file, a hash is
// verifiable on its own. bcrypt resists a dictionary attack on a human-chosen
// password; `$sha256$` costs ~1µs to verify and is meant for a generated secret,
// where the entropy — not the hashing cost — is what makes guessing hopeless.
package webcred

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MaxCredentials caps how many credentials one source may hold. Every
	// failed attempt is checked against all of them, so the list length is a
	// cost multiplier, not just a matter of taste.
	MaxCredentials = 64

	// PrefixSHA256 introduces a salted SHA-256 credential:
	// `$sha256$<salt>$<digest>`, both fields base64 (raw, unpadded).
	PrefixSHA256 = "$sha256$"

	// hashPrefix marks a hashed credential. A cleartext password may contain a
	// `$` but cannot start with one; that is the whole detection rule.
	hashPrefix = "$"
	// commentPrefix starts a whole-line comment. It only applies to whole
	// lines: a cleartext credential may legitimately contain a `#`.
	commentPrefix = "#"
	// fieldSeparator separates the fields of a `$sha256$` credential.
	fieldSeparator = "$"
	// sha256Fields is the field count of `$sha256$<salt>$<digest>` after the
	// prefix is removed.
	sha256Fields = 2

	// saltLen is the length in bytes of a generated `$sha256$` salt. It exists
	// to defeat precomputed tables, so 128 bits is ample.
	saltLen = 16

	// slowVerifySlots bounds how many bcrypt verifications run at once. bcrypt
	// is deliberately expensive and Basic auth travels on every request, so
	// without a bound a burst of wrong passwords would spend the whole machine
	// on hashing. Queued requests wait; a client that gives up frees its slot.
	slowVerifySlots = 2

	// cacheEntries and cacheTTL bound the verification cache: enough entries
	// for the operators and tools of one host, and a lifetime short enough that
	// a rotated credential stops working promptly.
	cacheEntries = 64
	cacheTTL     = 5 * time.Minute
)

// bcryptPrefixes lists the modular-crypt prefixes of the bcrypt variants
// golang.org/x/crypto/bcrypt verifies.
var bcryptPrefixes = [...]string{"$2a$", "$2b$", "$2y$"}

// kind is how one credential is stored, which decides how it is verified.
type kind uint8

const (
	kindPlain kind = iota
	kindBcrypt
	kindSHA256
)

// credential is one parsed entry of a source.
type credential struct {
	kind kind
	// plain holds a cleartext credential (kindPlain).
	plain string
	// hash holds the bcrypt modular-crypt string (kindBcrypt).
	hash []byte
	// salt and digest hold the two `$sha256$` fields (kindSHA256).
	salt   []byte
	digest []byte
}

// List is a parsed set of credentials for one role. The zero value is a valid
// empty list that never grants access.
type List struct {
	credentials []credential
	// cache and slots are only allocated when the list holds a bcrypt entry:
	// they exist to bound the cost of the expensive path, and a cleartext or
	// `$sha256$` list has none to bound. Pointers, so a copied List (Auth is
	// passed by value) shares one cache instead of silently losing it.
	cache *verifyCache
	slots chan struct{}
}

// Parse reads the contents of a credential file: one credential per line, with
// blank lines and `#` comments ignored. A trailing `# comment` is also stripped
// from a hashed line, where no whitespace can be part of the credential.
func Parse(data string) (List, error) {
	var (
		list List
		err  error
	)
	for i, raw := range strings.Split(data, "\n") {
		text := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if text == "" || strings.HasPrefix(text, commentPrefix) {
			continue
		}
		cred, parseErr := parseCredential(text)
		if parseErr != nil {
			return List{}, fmt.Errorf("line %d: %w", i+1, parseErr)
		}
		if list, err = list.add(cred); err != nil {
			return List{}, fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	if len(list.credentials) == 0 {
		return List{}, errNoCredentials
	}
	return list, nil
}

// ParseInline reads a single credential written directly in sermo.yml. It is
// not a file, so there are no comments or blank lines to skip: the value is the
// credential, hashed or not.
func ParseInline(secret string) (List, error) {
	if strings.TrimSpace(secret) == "" {
		return List{}, errNoCredentials
	}
	cred, err := parseCredential(secret)
	if err != nil {
		return List{}, err
	}
	var list List
	return list.add(cred)
}

// Plain builds a cleartext list. It is the programmatic equivalent of an inline
// password and keeps callers that already hold a secret (tests, fixtures) off
// the parsing path.
func Plain(secrets ...string) List {
	var list List
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		list.credentials = append(list.credentials, credential{kind: kindPlain, plain: secret})
	}
	return list
}

// errNoCredentials is returned for a source with nothing usable in it — an empty
// file, or one holding only blank lines and comments. Taken literally that would
// be an empty password, which would leave the dashboard open.
var errNoCredentials = errors.New("holds no credential")

// add returns the list with cred appended. It is a value method so every method
// on List keeps the same receiver shape, and so a half-built list is never
// visible to a caller.
func (l List) add(cred credential) (List, error) {
	if len(l.credentials) >= MaxCredentials {
		return List{}, fmt.Errorf("more than %d credentials", MaxCredentials)
	}
	l.credentials = append(l.credentials, cred)
	if cred.kind == kindBcrypt && l.cache == nil {
		l.cache = newVerifyCache()
		l.slots = make(chan struct{}, slowVerifySlots)
	}
	return l, nil
}

// parseCredential classifies one already-trimmed line.
func parseCredential(text string) (credential, error) {
	if !strings.HasPrefix(text, hashPrefix) {
		return credential{kind: kindPlain, plain: text}, nil
	}
	// Whitespace cannot occur inside a hash, so the remainder is a comment.
	hash := strings.Fields(text)[0]
	switch {
	case hasBcryptPrefix(hash):
		if _, err := bcrypt.Cost([]byte(hash)); err != nil {
			return credential{}, fmt.Errorf("malformed bcrypt credential: %w", err)
		}
		return credential{kind: kindBcrypt, hash: []byte(hash)}, nil
	case strings.HasPrefix(hash, PrefixSHA256):
		return parseSHA256(hash)
	default:
		// Never fall back to treating it as a cleartext password: a mistyped
		// hash would silently become the password itself.
		return credential{}, fmt.Errorf("unsupported hash format %q (expected %s or a bcrypt %s hash)",
			hashFormatName(hash), PrefixSHA256, bcryptPrefixes[0])
	}
}

func parseSHA256(hash string) (credential, error) {
	fields := strings.Split(strings.TrimPrefix(hash, PrefixSHA256), fieldSeparator)
	if len(fields) != sha256Fields {
		return credential{}, fmt.Errorf("malformed %s credential (expected %ssalt$digest)", PrefixSHA256, PrefixSHA256)
	}
	salt, err := base64.RawStdEncoding.DecodeString(fields[0])
	if err != nil {
		return credential{}, fmt.Errorf("malformed %s salt: %w", PrefixSHA256, err)
	}
	digest, err := base64.RawStdEncoding.DecodeString(fields[1])
	if err != nil {
		return credential{}, fmt.Errorf("malformed %s digest: %w", PrefixSHA256, err)
	}
	if len(digest) != sha256.Size {
		return credential{}, fmt.Errorf("malformed %s digest (expected %d bytes, got %d)", PrefixSHA256, sha256.Size, len(digest))
	}
	return credential{kind: kindSHA256, salt: salt, digest: digest}, nil
}

func hasBcryptPrefix(hash string) bool {
	for _, prefix := range bcryptPrefixes {
		if strings.HasPrefix(hash, prefix) {
			return true
		}
	}
	return false
}

// hashFormatName returns the `$name$` part of an unrecognized hash, so the error
// names the format without echoing the whole credential into a log or an issue.
func hashFormatName(hash string) string {
	rest := strings.TrimPrefix(hash, hashPrefix)
	if i := strings.Index(rest, fieldSeparator); i >= 0 {
		return hashPrefix + rest[:i+1]
	}
	return hashPrefix
}

// String redacts the credentials. A List holds cleartext passwords, so it must
// never render them into a log line, an error or a test failure message just
// because someone formatted the struct that carries it.
func (l List) String() string {
	return fmt.Sprintf("webcred.List(%d credentials)", len(l.credentials))
}

// Empty reports whether the list grants nothing.
func (l List) Empty() bool { return len(l.credentials) == 0 }

// Len returns how many credentials the list holds.
func (l List) Len() int { return len(l.credentials) }

// Plaintext returns the single cleartext credential of the list, if that is all
// it holds. sermoctl needs an actual password to authenticate against the daemon
// API, which a hashed list cannot supply.
func (l List) Plaintext() (string, bool) {
	if len(l.credentials) != 1 || l.credentials[0].kind != kindPlain {
		return "", false
	}
	return l.credentials[0].plain, true
}

// Verify reports whether password matches any credential in the list. ctx must
// be non-nil; it is honored while queueing for an expensive verification, so a
// client that disconnects stops costing CPU.
//
// Every credential is checked even after a match: leaving the loop early would
// leak, through response time, both how many credentials exist and which one
// matched.
func (l List) Verify(ctx context.Context, password string) bool {
	if len(l.credentials) == 0 || password == "" {
		return false
	}
	if l.cache == nil {
		return l.check(password)
	}
	if match, found := l.cache.get(password); found {
		return match
	}
	select {
	case l.slots <- struct{}{}:
		defer func() { <-l.slots }()
	case <-ctx.Done():
		return false
	}
	// Another request may have verified the same password while this one
	// queued for a slot.
	if match, found := l.cache.get(password); found {
		return match
	}
	match := l.check(password)
	l.cache.put(password, match)
	return match
}

func (l List) check(password string) bool {
	match := false
	for _, cred := range l.credentials {
		if cred.verify(password) {
			match = true
		}
	}
	return match
}

func (c credential) verify(password string) bool {
	switch c.kind {
	case kindPlain:
		return SecureEqual(c.plain, password)
	case kindSHA256:
		digest := saltedDigest(c.salt, password)
		return subtle.ConstantTimeCompare(digest[:], c.digest) == 1
	case kindBcrypt:
		return bcrypt.CompareHashAndPassword(c.hash, []byte(password)) == nil
	}
	return false
}

// saltedDigest is the `$sha256$` digest of password under salt. It is the one
// definition shared by verification and by HashSHA256, so the two can never
// drift apart. Writing into the hash avoids joining salt and password into a
// throwaway buffer on a path that runs per request.
func saltedDigest(salt []byte, password string) [sha256.Size]byte {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(password))
	var digest [sha256.Size]byte
	h.Sum(digest[:0])
	return digest
}

// SecureEqual compares two secrets in constant time. Both are hashed first so
// the comparison cannot leak their length.
func SecureEqual(a, b string) bool {
	ah := sha256.Sum256([]byte(a))
	bh := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
}

// verifyCache remembers recent bcrypt verdicts, keyed by the digest of the
// attempted password. Failures are cached too: without that, a client looping
// on a wrong password would pay the full bcrypt cost on every request.
type verifyCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]verdict
	now     func() time.Time
}

type verdict struct {
	match   bool
	expires time.Time
}

func newVerifyCache() *verifyCache {
	return &verifyCache{entries: make(map[[sha256.Size]byte]verdict, cacheEntries), now: time.Now}
}

func (c *verifyCache) get(password string) (match, found bool) {
	key := sha256.Sum256([]byte(password))
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return false, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.entries, key)
		return false, false
	}
	return entry.match, true
}

func (c *verifyCache) put(password string, match bool) {
	key := sha256.Sum256([]byte(password))
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= cacheEntries {
		c.evict()
	}
	c.entries[key] = verdict{match: match, expires: c.now().Add(cacheTTL)}
}

// evict drops expired entries, and the entry closest to expiring when none are.
// Dropping the whole cache instead would let a flood of wrong passwords push
// every operator back onto the slow path.
func (c *verifyCache) evict() {
	now := c.now()
	var oldestKey [sha256.Size]byte
	var oldest time.Time
	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
			continue
		}
		if oldest.IsZero() || entry.expires.Before(oldest) {
			oldestKey, oldest = key, entry.expires
		}
	}
	if len(c.entries) >= cacheEntries && !oldest.IsZero() {
		delete(c.entries, oldestKey)
	}
}
