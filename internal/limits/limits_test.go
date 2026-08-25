package limits

import (
	"sync"
	"testing"
	"time"
)

func TestTransferLimiterGlobalAndClientCeilings(t *testing.T) {
	limiter := NewTransferLimiter(3, 2)

	releaseA1, rejection := limiter.Acquire("a")
	if rejection != TransferAdmitted {
		t.Fatalf("first a admission rejected: %v", rejection)
	}
	releaseA2, rejection := limiter.Acquire("a")
	if rejection != TransferAdmitted {
		t.Fatalf("second a admission rejected: %v", rejection)
	}
	if _, rejection := limiter.Acquire("a"); rejection != TransferRejectedClient {
		t.Fatalf("third a rejection = %v; want client", rejection)
	}

	releaseB, rejection := limiter.Acquire("b")
	if rejection != TransferAdmitted {
		t.Fatalf("b admission rejected: %v", rejection)
	}
	if _, rejection := limiter.Acquire("c"); rejection != TransferRejectedGlobal {
		t.Fatalf("global rejection = %v; want global", rejection)
	}

	releaseA1()
	releaseA1() // release is idempotent
	if limiter.Active() != 2 || limiter.ActiveFor("a") != 1 {
		t.Fatalf("active=%d active(a)=%d; want 2 and 1", limiter.Active(), limiter.ActiveFor("a"))
	}
	releaseA2()
	releaseB()
	if limiter.Active() != 0 {
		t.Fatalf("active=%d; want 0", limiter.Active())
	}
}

func TestTransferLimiterConcurrentRelease(t *testing.T) {
	limiter := NewTransferLimiter(1, 1)
	release, rejection := limiter.Acquire("client")
	if rejection != TransferAdmitted {
		t.Fatal("initial admission rejected")
	}

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release()
		}()
	}
	wg.Wait()
	if limiter.Active() != 0 {
		t.Fatalf("active=%d; want 0", limiter.Active())
	}
}

func TestByteQuotaReservesAndResets(t *testing.T) {
	now := time.Unix(100, 0)
	quota := newByteQuota(100, time.Minute, func() time.Time { return now })

	if result := quota.Reserve("a", 60); !result.Allowed || result.Remaining != 40 {
		t.Fatalf("first reserve = %+v", result)
	}
	if result := quota.Reserve("a", 41); result.Allowed || result.Remaining != 40 {
		t.Fatalf("overflow reserve = %+v", result)
	}
	if result := quota.Reserve("b", 100); !result.Allowed || result.Remaining != 0 {
		t.Fatalf("separate key reserve = %+v", result)
	}

	now = now.Add(time.Minute)
	if result := quota.Reserve("a", 100); !result.Allowed || result.Remaining != 0 {
		t.Fatalf("post-reset reserve = %+v", result)
	}
}

func TestByteQuotaChargeExhaustsCurrentWindow(t *testing.T) {
	now := time.Unix(100, 0)
	quota := newByteQuota(100, time.Minute, func() time.Time { return now })
	if result := quota.Charge("a", 80); !result.Allowed || result.Remaining != 20 {
		t.Fatalf("first charge = %+v", result)
	}
	if result := quota.Charge("a", 30); result.Allowed || result.Remaining != 0 {
		t.Fatalf("crossing charge = %+v", result)
	}
	if got := quota.Used("a"); got != 100 {
		t.Fatalf("used=%d; want 100", got)
	}
}

func TestKeyedRateLimiterRefills(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newKeyedRateLimiter(2, 2, func() time.Time { return now })

	if allowed, _ := limiter.Allow("a"); !allowed {
		t.Fatal("first token rejected")
	}
	if allowed, _ := limiter.Allow("a"); !allowed {
		t.Fatal("second token rejected")
	}
	if allowed, retry := limiter.Allow("a"); allowed || retry <= 0 {
		t.Fatalf("empty bucket allowed=%v retry=%v", allowed, retry)
	}
	if allowed, _ := limiter.Allow("b"); !allowed {
		t.Fatal("separate key rejected")
	}

	now = now.Add(500 * time.Millisecond)
	if allowed, _ := limiter.Allow("a"); !allowed {
		t.Fatal("refilled token rejected")
	}
}

func TestByteQuotaBoundsTrackedClientState(t *testing.T) {
	now := time.Unix(100, 0)
	quota := newByteQuota(100, time.Hour, func() time.Time { return now })
	quota.maxEntries = 2
	if !quota.Reserve("a", 1).Allowed {
		t.Fatal("reserve a rejected")
	}
	now = now.Add(time.Second)
	if !quota.Reserve("b", 1).Allowed {
		t.Fatal("reserve b rejected")
	}
	now = now.Add(time.Second)
	if !quota.Reserve("c", 1).Allowed {
		t.Fatal("reserve c rejected")
	}
	if got := len(quota.entries); got != 2 {
		t.Fatalf("tracked quota entries=%d; want 2", got)
	}
	if _, exists := quota.entries["a"]; exists {
		t.Fatal("oldest quota entry was not evicted")
	}
}

func TestKeyedRateLimiterBoundsTrackedClientState(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newKeyedRateLimiter(1, 1, func() time.Time { return now })
	limiter.maxEntries = 2
	for _, key := range []string{"a", "b", "c"} {
		if allowed, _ := limiter.Allow(key); !allowed {
			t.Fatalf("first request for %s rejected", key)
		}
		now = now.Add(time.Second)
	}
	if got := len(limiter.states); got != 2 {
		t.Fatalf("tracked rate entries=%d; want 2", got)
	}
	if _, exists := limiter.states["a"]; exists {
		t.Fatal("oldest rate entry was not evicted")
	}
}
