package measurementclock

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32       = windows.NewLazySystemDLL("kernel32.dll")
	queryCounter   = kernel32.NewProc("QueryPerformanceCounter")
	queryFrequency = kernel32.NewProc("QueryPerformanceFrequency")
	frequency      = readCounter(queryFrequency)
	epoch          = time.Now()
	origin         = readCounter(queryCounter)
)

func readCounter(procedure *windows.LazyProc) uint64 {
	var value int64
	ok, _, _ := procedure.Call(uintptr(unsafe.Pointer(&value)))
	if ok == 0 || value <= 0 {
		panic("measurement clock: Windows performance counter unavailable")
	}
	return uint64(value)
}

// Now anchors QPC's monotonic intervals to one civil/monotonic epoch. Its time
// values support ordinary Sub/Before/Add without inventing a minimum duration.
// Go's own Windows benchmarks likewise use QPC for short intervals:
// https://go.dev/src/testing/testing_windows.go
func Now() time.Time {
	return epoch.Add(counterDuration(readCounter(queryCounter)-origin, frequency))
}
