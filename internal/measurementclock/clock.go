// Package measurementclock supplies high-resolution, monotonic measurement
// timestamps. Use Now/Sub or Since for intervals; time.Now remains the source
// for civil timestamps, credentials, and scheduling unrelated to measurement.
package measurementclock

import (
	"math/bits"
	"time"
)

// Since must be paired with Now: Windows measurement timestamps do not use the
// Go runtime's coarse clock, so time.Since is not interchangeable with Since.
func Since(start time.Time) time.Duration { return Now().Sub(start) }

// counterDuration uses a wide product so QPC conversion neither overflows for
// long-running processes nor loses precision by converting uptime to float64.
func counterDuration(delta, frequency uint64) time.Duration {
	hi, lo := bits.Mul64(delta, uint64(time.Second))
	nanoseconds, _ := bits.Div64(hi, lo, frequency)
	return time.Duration(nanoseconds)
}
