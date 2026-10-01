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

func TestByteQuotaWriteReservationSettlesActualBytesOnce(t *testing.T) {
	quota := NewByteQuota(100, time.Minute)
	result, settle := quota.ReserveWrite("a", 80)
	if !result.Allowed || quota.Used("a") != 80 {
		t.Fatal("write was not reserved atomically")
	}
	settle(30)
	settle(0)
	if got := quota.Used("a"); got != 30 {
		t.Fatalf("settled charge=%d; want 30", got)
	}
	if quota.Reserve("a", 71).Allowed {
		t.Fatal("settlement credited more than unwritten bytes")
	}
}

func TestByteQuotaWriteSettlementPreservesConcurrentCharge(t *testing.T) {
	for _, test := range []struct {
		name          string
		read, written int64
		wantUsed      int64
	}{
		{"failed write", 10, 0, 90},
		{"partial write", 10, 5, 95},
		{"complete write", 10, 20, 100},
		{"read exhausts actual allowance", 30, 0, 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			quota := NewByteQuota(100, time.Minute)
			quota.Charge("client", 80)
			result, settle := quota.ReserveWrite("client", 20)
			if !result.Allowed {
				t.Fatal("initial write reservation rejected")
			}
			// A second session consumes bytes while the first session's write
			// is still reserved. Its denied read must remain charged.
			charged := make(chan QuotaResult, 1)
			go func() { charged <- quota.Charge("client", test.read) }()
			if result := <-charged; result.Allowed || result.Remaining != 0 {
				t.Fatalf("concurrent read = %+v; want denied with no allowance", result)
			}
			wantCommitted := min(int64(100), 80+test.read)
			if entry := quota.entries["client"]; entry.committed != wantCommitted || entry.reserved != 20 {
				t.Fatalf("read mixed consumption with pending writes: %+v", entry)
			}
			settle(test.written)
			if got := quota.Used("client"); got != test.wantUsed {
				t.Fatalf("settled usage=%d; want %d", got, test.wantUsed)
			}
			if entry := quota.entries["client"]; entry.committed != test.wantUsed || entry.reserved != 0 {
				t.Fatalf("settlement did not commit and release its write: %+v", entry)
			}
			remaining := int64(100) - test.wantUsed
			if quota.Reserve("client", remaining+1).Allowed {
				t.Fatal("later transfer reclaimed consumed bytes")
			}
			if result, settleAgain := quota.ReserveWrite("client", remaining+1); result.Allowed {
				settleAgain(0)
				t.Fatal("later write reclaimed consumed bytes")
			}
			if remaining > 0 && !quota.Reserve("client", remaining).Allowed {
				t.Fatal("unused reservation was not released")
			}
		})
	}
}

func TestByteQuotaConcurrentSettlementsKeepChargedBytes(t *testing.T) {
	quota := NewByteQuota(100, time.Minute)
	quota.Charge("client", 80)
	var settlements []func(int64)
	for i := 0; i < 20; i++ {
		result, settle := quota.ReserveWrite("client", 1)
		if !result.Allowed {
			t.Fatal("initial write reservation rejected")
		}
		settlements = append(settlements, settle)
	}
	if quota.Charge("client", 20).Allowed {
		t.Fatal("read was admitted despite pending writes")
	}
	var wg sync.WaitGroup
	for _, settle := range settlements {
		wg.Add(1)
		go func() { defer wg.Done(); settle(0) }()
	}
	wg.Wait()
	if quota.Used("client") != 100 || quota.Reserve("client", 1).Allowed {
		t.Fatal("concurrent failed writes erased consumed bytes")
	}
}

func TestByteQuotaWriteSettlementCannotOverflowCommittedBytes(t *testing.T) {
	const maximum = int64(1<<63 - 1)
	quota := NewByteQuota(maximum, time.Minute)
	quota.Charge("client", maximum-15)
	_, settle := quota.ReserveWrite("client", 10)
	if result := quota.Charge("client", maximum); result.Allowed || result.Remaining != 0 {
		t.Fatalf("overflowing read = %+v", result)
	}
	settle(7)
	if quota.Used("client") != maximum || quota.Reserve("client", 1).Allowed {
		t.Fatal("large read or settlement overflowed committed usage")
	}
}

func TestByteQuotaWriteSettlementCannotCreditNewWindowOrRecreatedEntry(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		now := time.Unix(100, 0)
		quota := newByteQuota(100, time.Minute, func() time.Time { return now })
		_, settle := quota.ReserveWrite("a", 80)
		if recreate {
			// Force eviction and recreation without advancing the clock. A
			// timestamp alone cannot distinguish these reservations.
			quota.maxEntries = 1
			quota.Reserve("b", 10)
		} else {
			now = now.Add(time.Minute)
		}
		quota.Reserve("a", 25)
		settle(0)
		if got := quota.Used("a"); got != 25 {
			t.Fatalf("recreate=%v: old settlement credited new charge: %d", recreate, got)
		}
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
