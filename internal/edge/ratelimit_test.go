package edge

import (
	"testing"
	"time"
)

func TestIPLimiterAllowsUpToBurstThenBlocks(t *testing.T) {
	l := newIPLimiter(1, 3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("expected request %d to be allowed within burst", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("expected 4th request to be blocked once burst is exhausted")
	}
}

func TestIPLimiterKeysAreIndependent(t *testing.T) {
	// This is the scenario the design review flagged: webhook providers like
	// Stripe send from a shared pool of source IPs used across *all* their
	// customers. If two different tenants' traffic shared one bucket keyed
	// on IP alone, one tenant's volume would throttle another's webhooks.
	// Keying on "IP|subdomain" instead must keep them fully independent.
	l := newIPLimiter(1, 2, time.Minute)
	const sharedIP = "203.0.113.9" // simulates a webhook provider's shared egress IP

	keyA := sharedIP + "|tenant-a"
	keyB := sharedIP + "|tenant-b"

	// Exhaust tenant A's bucket completely.
	if !l.Allow(keyA) || !l.Allow(keyA) {
		t.Fatal("expected tenant A's first two requests (within burst) to be allowed")
	}
	if l.Allow(keyA) {
		t.Fatal("expected tenant A to be rate limited after exhausting its burst")
	}

	// Tenant B, sharing the same source IP, must be unaffected.
	if !l.Allow(keyB) {
		t.Fatal("tenant B should not be rate limited by tenant A's traffic on the same source IP")
	}
	if !l.Allow(keyB) {
		t.Fatal("tenant B should still have its own full burst available")
	}
}

func TestIPLimiterRefillsOverTime(t *testing.T) {
	l := newIPLimiter(1000, 1, time.Minute) // fast refill so the test doesn't sleep long
	if !l.Allow("1.2.3.4") {
		t.Fatal("expected first request to be allowed")
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("expected immediate second request to be blocked (burst of 1)")
	}
	time.Sleep(5 * time.Millisecond)
	if !l.Allow("1.2.3.4") {
		t.Fatal("expected the bucket to have refilled after waiting")
	}
}

func TestIPLimiterEvictsIdleEntries(t *testing.T) {
	l := newIPLimiter(1, 1, 10*time.Millisecond)
	l.Allow("1.2.3.4")
	if len(l.entries) != 1 {
		t.Fatalf("expected 1 tracked entry, got %d", len(l.entries))
	}

	time.Sleep(20 * time.Millisecond)
	// A call for a different key triggers the sweep and should evict the
	// now-idle "1.2.3.4" entry, leaving only the new key behind.
	l.Allow("5.6.7.8")
	if _, ok := l.entries["1.2.3.4"]; ok {
		t.Fatal("expected idle entry to be evicted after idleTTL elapsed")
	}
	if _, ok := l.entries["5.6.7.8"]; !ok {
		t.Fatal("expected the just-used key to still be tracked")
	}
}
