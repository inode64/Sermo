package webcred

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// hashOrFail hashes password at the cheapest bcrypt cost: these tests assert on
// parsing and verification, not on the work factor.
func hashOrFail(t *testing.T, password string) string {
	t.Helper()
	line, err := HashBcrypt(password, MinBcryptCost)
	if err != nil {
		t.Fatalf("HashBcrypt() error = %v", err)
	}
	return line
}

func sha256OrFail(t *testing.T, password string) string {
	t.Helper()
	line, err := HashSHA256(password)
	if err != nil {
		t.Fatalf("HashSHA256() error = %v", err)
	}
	return line
}

func TestParse(t *testing.T) {
	bcryptLine := hashOrFail(t, "typed")
	sha256Line := sha256OrFail(t, "generated")

	tests := []struct {
		name    string
		data    string
		wantLen int
		accepts []string
		rejects []string
	}{
		{
			name:    "single bcrypt line, trailing newline trimmed",
			data:    bcryptLine + "\n",
			wantLen: 1,
			accepts: []string{"typed"},
			rejects: []string{"typed\n", " typed", "other", ""},
		},
		{
			name:    "blank lines and comments are skipped",
			data:    "\n# admins\n\n  # indented comment\n" + bcryptLine + "\n",
			wantLen: 1,
			accepts: []string{"typed"},
			rejects: []string{"# admins", ""},
		},
		{
			name:    "every line is a credential",
			data:    bcryptLine + "\n" + sha256Line + "\n",
			wantLen: 2,
			accepts: []string{"typed", "generated"},
			rejects: []string{"other"},
		},
		{
			name:    "hashed lines take a trailing comment",
			data:    bcryptLine + "   # ana\n" + sha256Line + "\t# cron\n",
			wantLen: 2,
			accepts: []string{"typed", "generated"},
			rejects: []string{"ana", bcryptLine, "wrong"},
		},
		{
			name:    "carriage returns are stripped",
			data:    bcryptLine + "\r\n" + sha256Line + "\r\n",
			wantLen: 2,
			accepts: []string{"typed", "generated"},
			rejects: []string{"typed\r"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			list, err := Parse(tc.data)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got := len(list.credentials); got != tc.wantLen {
				t.Errorf("Parse() len = %d, want %d", got, tc.wantLen)
			}
			assertVerdicts(t, list, tc.accepts, tc.rejects)
		})
	}
}

// assertVerdicts checks that list accepts every password in accepts and refuses
// every one in rejects.
func assertVerdicts(t *testing.T, list List, accepts, rejects []string) {
	t.Helper()
	for _, password := range accepts {
		if !list.Verify(t.Context(), password) {
			t.Errorf("Verify(%q) = false, want true", password)
		}
	}
	for _, password := range rejects {
		if list.Verify(t.Context(), password) {
			t.Errorf("Verify(%q) = true, want false", password)
		}
	}
}

// A source that cannot be parsed grants nothing: the error names the line, and
// no credential survives it.
func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{
			name:    "empty source",
			data:    "",
			wantErr: "holds no credential",
		},
		{
			name:    "only comments and blanks",
			data:    "# nothing here\n\n   \n",
			wantErr: "holds no credential",
		},
		{
			name:    "unknown hash format is never a cleartext password",
			data:    "$md5$deadbeef\n",
			wantErr: `line 1: unsupported hash format "$md5$"`,
		},
		{
			name:    "malformed bcrypt is rejected with its line",
			data:    hashOrFail(t, "ok") + "\n$2a$12$tooshort\n",
			wantErr: "line 2: malformed bcrypt credential",
		},
		{
			name:    "malformed sha256 fields",
			data:    PrefixSHA256 + "onlyonefield\n",
			wantErr: "line 1: malformed " + PrefixSHA256 + " credential",
		},
		{
			name:    "sha256 digest of the wrong length",
			data:    PrefixSHA256 + "c2FsdA$c2hvcnQ\n",
			wantErr: "line 1: malformed " + PrefixSHA256 + " digest (expected 32 bytes, got 5)",
		},
		{
			name:    "more credentials than the limit",
			data:    strings.Repeat(hashOrFail(t, "pw")+"\n", MaxCredentials+1),
			wantErr: "more than 64 credentials",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			list, err := Parse(tc.data)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Parse() error = %v, want it to contain %q", err, tc.wantErr)
			}
			if !list.Empty() {
				t.Errorf("Parse() returned %d credentials with an error", len(list.credentials))
			}
		})
	}
}

func TestMaxCredentialsIsAccepted(t *testing.T) {
	list, err := Parse(strings.Repeat(hashOrFail(t, "pw")+"\n", MaxCredentials))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got := len(list.credentials); got != MaxCredentials {
		t.Fatalf("Parse() len = %d, want %d", got, MaxCredentials)
	}
}

func TestZeroListGrantsNothing(t *testing.T) {
	var list List
	if !list.Empty() {
		t.Fatalf("zero List is not empty")
	}
	if list.Verify(t.Context(), "anything") {
		t.Error("zero List accepted a password")
	}
}

func TestParseRejectsPlaintext(t *testing.T) {
	for _, input := range []string{"s3cret", "first\nsecond\n", "name=s3cret"} {
		if _, err := Parse(input); err == nil || !strings.Contains(err.Error(), "plaintext credential") {
			t.Errorf("Parse(%q) error = %v, want plaintext rejection", input, err)
		}
	}
}

// A bcrypt list caches both verdicts: the expensive path must not be paid again
// for a password already seen, right or wrong.
func TestVerifyCachesBothVerdicts(t *testing.T) {
	list, err := Parse(hashOrFail(t, "typed"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if list.cache == nil {
		t.Fatal("a bcrypt list has no verification cache")
	}
	for _, tc := range []struct {
		password string
		want     bool
	}{{"typed", true}, {"wrong", false}} {
		if got := list.Verify(t.Context(), tc.password); got != tc.want {
			t.Fatalf("Verify(%q) = %v, want %v", tc.password, got, tc.want)
		}
		cached, found := list.cache.get(tc.password)
		if !found || cached != tc.want {
			t.Errorf("cache for %q = %v, found %v; want %v, true", tc.password, cached, found, tc.want)
		}
		// The cached verdict is what a repeated attempt gets.
		if got := list.Verify(t.Context(), tc.password); got != tc.want {
			t.Errorf("repeated Verify(%q) = %v, want %v", tc.password, got, tc.want)
		}
	}
}

// A sha256 list has nothing expensive to cache, so it keeps no record of the
// passwords it was offered.
func TestSHA256ListHasNoCache(t *testing.T) {
	data := sha256OrFail(t, "generated") + "\n"
	list, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if list.cache != nil || list.slots != nil {
		t.Errorf("Parse(%q) allocated a cache for a fast list", data)
	}
}

func TestCacheEntriesExpire(t *testing.T) {
	list, err := Parse(hashOrFail(t, "typed"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	now := time.Now()
	list.cache.now = func() time.Time { return now }
	if !list.Verify(t.Context(), "typed") {
		t.Fatal("Verify() = false, want true")
	}
	now = now.Add(cacheTTL + time.Second)
	if _, found := list.cache.get("typed"); found {
		t.Error("cache entry survived its TTL")
	}
	// A rotated credential stops working once the entry is gone; the password
	// still matches here, so the verdict is recomputed rather than remembered.
	if !list.Verify(t.Context(), "typed") {
		t.Error("Verify() after expiry = false, want true")
	}
}

func TestCacheStaysBounded(t *testing.T) {
	list, err := Parse(hashOrFail(t, "typed"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	now := time.Now()
	list.cache.now = func() time.Time { return now }
	for i := range cacheEntries * 2 {
		// Distinct passwords, as a flood of wrong guesses would be.
		list.cache.put(strings.Repeat("x", i+1), false)
		now = now.Add(time.Second)
	}
	if got := len(list.cache.entries); got > cacheEntries {
		t.Errorf("cache holds %d entries, want at most %d", got, cacheEntries)
	}
}

// A client that gives up while queued for an expensive verification must not
// hold a slot: Verify returns instead of waiting.
func TestVerifyHonorsContextWhileQueued(t *testing.T) {
	list, err := Parse(hashOrFail(t, "typed"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for range cap(list.slots) {
		list.slots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if list.Verify(ctx, "typed") {
		t.Error("Verify() with a cancelled context = true, want false")
	}
}

func TestHashRoundTrip(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret() error = %v", err)
	}
	if len(secret) < secretLen {
		t.Errorf("GenerateSecret() = %q, want at least %d characters", secret, secretLen)
	}
	other, err := GenerateSecret()
	if err != nil || other == secret {
		t.Errorf("GenerateSecret() repeated itself (%v)", err)
	}

	for _, tc := range []struct {
		name string
		line string
	}{
		{name: webHashName(PrefixSHA256), line: sha256OrFail(t, secret)},
		{name: "bcrypt", line: hashOrFail(t, secret)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := Parse(tc.line)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !list.Verify(t.Context(), secret) {
				t.Error("Verify() with the hashed secret = false, want true")
			}
			if list.Verify(t.Context(), secret+"x") {
				t.Error("Verify() with a wrong secret = true, want false")
			}
		})
	}
}

func webHashName(prefix string) string { return strings.Trim(prefix, "$") }

func TestHashErrors(t *testing.T) {
	if _, err := HashBcrypt("", DefaultBcryptCost); err == nil {
		t.Error("HashBcrypt(\"\") = nil error, want one")
	}
	if _, err := HashSHA256(""); err == nil {
		t.Error("HashSHA256(\"\") = nil error, want one")
	}
	for _, cost := range []int{MinBcryptCost - 1, MaxBcryptCost + 1} {
		if _, err := HashBcrypt("pw", cost); err == nil {
			t.Errorf("HashBcrypt(cost=%d) = nil error, want one", cost)
		}
	}
}

func TestSecureEqual(t *testing.T) {
	if !SecureEqual("token", "token") {
		t.Error("SecureEqual() on equal secrets = false")
	}
	if SecureEqual("token", "token ") || SecureEqual("token", "") {
		t.Error("SecureEqual() on different secrets = true")
	}
}

// Formatting a List (or the struct that carries it) must never print a
// password: a single %v in a log line would undo the whole point of hashing.
func TestListStringRedactsCredentials(t *testing.T) {
	first := hashOrFail(t, "s3cret")
	second := hashOrFail(t, "second")
	list, err := Parse(first + "\n" + second + "\n")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	for _, rendered := range []string{
		list.String(),
		fmt.Sprintf("%v", list),
		fmt.Sprintf("%+v", struct{ Admin List }{list}),
		fmt.Sprint(struct {
			Admin List
			Guest List
		}{list, list}),
	} {
		if strings.Contains(rendered, first) || strings.Contains(rendered, second) {
			t.Errorf("rendered List = %q, want the credentials redacted", rendered)
		}
	}
}

// The on-disk format is a contract: every credential file already deployed must
// keep verifying. This vector is computed independently (sha256 of salt||
// password, both fields raw-standard base64), so a refactor of the hashing path
// cannot silently invalidate what operators have on disk.
func TestSHA256FormatVector(t *testing.T) {
	const (
		line     = "$sha256$c2FsdA$LQO8MKPHuIGU2KjPYT4rewzl0H9eA4NlRWhKiiHq5Ho"
		password = "s3cret"
	)
	list, err := Parse(line)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !list.Verify(t.Context(), password) {
		t.Error("the fixed $sha256$ vector no longer verifies: the on-disk format changed")
	}
	if list.Verify(t.Context(), password+"x") {
		t.Error("the fixed vector accepts a wrong password")
	}
	// Salt and password must not be interchangeable: hashing them in the wrong
	// order would still round-trip within this package but break every file.
	if list.Verify(t.Context(), "salt") {
		t.Error("the digest treats the salt as the password")
	}
}
