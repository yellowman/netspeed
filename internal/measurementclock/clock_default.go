//go:build !windows

package measurementclock

import "time"

// Now retains Go's monotonic clock on platforms with suitable resolution.
func Now() time.Time { return time.Now() }
