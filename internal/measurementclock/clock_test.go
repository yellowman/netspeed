package measurementclock

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestCounterDuration(t *testing.T) {
	for _, test := range []struct {
		delta, frequency uint64
		want             time.Duration
	}{
		{0, 10000000, 0},
		{1, 10000000, 100 * time.Nanosecond},
		{3, 2, 1500 * time.Millisecond},
		{864000000000, 10000000, 24 * time.Hour},
		{98765432101234, 10000000, 9876543210123400 * time.Nanosecond},
	} {
		if got := counterDuration(test.delta, test.frequency); got != test.want {
			t.Errorf("counterDuration(%d, %d) = %s; want %s", test.delta, test.frequency, got, test.want)
		}
	}
}

func TestShortIntervalAdvances(t *testing.T) {
	start := Now()
	var work atomic.Uint64
	for i := 0; i < 10000; i++ {
		work.Add(1)
	}
	end := Now()
	if duration := end.Sub(start); duration <= 0 {
		t.Fatalf("short interval did not advance: %s", duration)
	}
	if Since(start) < end.Sub(start) {
		t.Fatal("clock moved backwards")
	}
	if end.Before(start) {
		t.Fatal("timestamps are not ordered")
	}
}
