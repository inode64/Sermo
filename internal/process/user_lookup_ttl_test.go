package process

import (
	"testing"
	"time"
)

// TestUserLookupNegativeCacheExpires pins that a failed lookup is re-resolved
// after negTTL while a successful one stays cached. Caching a miss forever meant
// a user created after the first probe (e.g. one named in kill_only_if) was never
// recognized until the daemon restarted.
func TestUserLookupNegativeCacheExpires(t *testing.T) {
	l := NewUserLookup(UserLookupConfig{Mode: UserLookupNumeric})
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	l.negTTL = 30 * time.Second

	storeLookup(l, l.users, "ghost", lookupCacheResult[uint32]{ok: false})
	if _, cached := cachedLookup(l, l.users, "ghost"); !cached {
		t.Fatal("a fresh negative entry should be served from cache")
	}
	now = now.Add(31 * time.Second)
	if _, cached := cachedLookup(l, l.users, "ghost"); cached {
		t.Fatal("an expired negative entry must be re-resolved, not served from cache")
	}

	// Positive entries never expire.
	storeLookup(l, l.users, "root", lookupCacheResult[uint32]{value: 0, ok: true})
	now = now.Add(time.Hour)
	if _, cached := cachedLookup(l, l.users, "root"); !cached {
		t.Fatal("a positive entry must remain cached")
	}

	// The same applies to the name caches.
	storeLookup(l, l.userNames, 4242, lookupCacheResult[string]{ok: false})
	now = now.Add(31 * time.Second)
	if _, cached := cachedLookup(l, l.userNames, 4242); cached {
		t.Fatal("an expired negative name entry must be re-resolved")
	}
}
