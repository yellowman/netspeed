package turn

import (
	"testing"
	"time"
)

func TestByteTokenBucketEnforcesCombinedByteBudget(t *testing.T) {
	now := time.Unix(100, 0)
	bucket := newByteTokenBucketWithClock(8, func() time.Time { return now }) // 1 MB/s

	if !bucket.allow(1_000_000) {
		t.Fatal("initial one-second burst was rejected")
	}
	if bucket.allow(1) {
		t.Fatal("bucket admitted bytes after the burst was exhausted")
	}

	now = now.Add(500 * time.Millisecond)
	if !bucket.allow(500_000) {
		t.Fatal("half-second refill was rejected")
	}
	if bucket.allow(1) {
		t.Fatal("bucket admitted bytes beyond the half-second refill")
	}

	now = now.Add(10 * time.Second)
	if !bucket.allow(1_000_000) {
		t.Fatal("bucket did not refill to its one-second burst ceiling")
	}
	if bucket.allow(1) {
		t.Fatal("bucket exceeded its configured burst after a long idle period")
	}
}
