// Package measurement contains the statistical rules shared by the Go client
// and mirrored by the browser client. The functions deliberately use simple,
// specified algorithms so results are reproducible across runtimes.
package measurement

import (
	"math"
	"sort"
)

// Clean returns finite, strictly positive measurements while preserving order.
func Clean(values []float64) []float64 {
	clean := make([]float64, 0, len(values))
	for _, value := range values {
		if value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			clean = append(clean, value)
		}
	}
	return clean
}

// DropWarmup removes the first count valid measurements. It is used for
// unloaded latency, where the first two requests can include cold-start work.
func DropWarmup(values []float64, count int) []float64 {
	clean := Clean(values)
	if count <= 0 {
		return clean
	}
	if count >= len(clean) {
		return nil
	}
	return append([]float64(nil), clean[count:]...)
}

// Percentile computes an R-7 linearly interpolated percentile. R-7 is the
// default algorithm in several statistical tools and is straightforward to
// reproduce in JavaScript: rank = p/100 * (n-1).
func Percentile(values []float64, p float64) float64 {
	clean := Clean(values)
	if len(clean) == 0 {
		return 0
	}
	sort.Float64s(clean)
	return percentileSorted(clean, p)
}

func percentileSorted(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}

	rank := (p / 100) * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	weight := rank - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}

// FilterIQR removes values outside 1.5 times the interquartile range. If that
// would remove more than half the valid samples, it returns the valid input
// unchanged rather than manufacturing a tiny, unstable result set.
func FilterIQR(values []float64) []float64 {
	clean := Clean(values)
	if len(clean) < 4 {
		return clean
	}

	sorted := append([]float64(nil), clean...)
	sort.Float64s(sorted)
	q1 := percentileSorted(sorted, 25)
	q3 := percentileSorted(sorted, 75)
	iqr := q3 - q1
	lower := q1 - 1.5*iqr
	upper := q3 + 1.5*iqr

	filtered := make([]float64, 0, len(clean))
	for _, value := range clean {
		if value >= lower && value <= upper {
			filtered = append(filtered, value)
		}
	}
	if len(filtered)*2 < len(clean) {
		return clean
	}
	return filtered
}

// PrepareLatency applies the shared latency preprocessing rule: discard the
// requested number of valid warm-up probes, then apply the conservative IQR
// filter.
func PrepareLatency(values []float64, warmupCount int) []float64 {
	return FilterIQR(DropWarmup(values, warmupCount))
}

// Jitter is defined consistently as p90 minus median.
func Jitter(values []float64) float64 {
	clean := Clean(values)
	if len(clean) == 0 {
		return 0
	}
	return Percentile(clean, 90) - Percentile(clean, 50)
}

// CoefficientOfVariation returns population standard deviation divided by the
// mean, expressed as a percentage.
func CoefficientOfVariation(values []float64) float64 {
	clean := Clean(values)
	if len(clean) < 2 {
		return 0
	}
	var sum float64
	for _, value := range clean {
		sum += value
	}
	mean := sum / float64(len(clean))
	if mean <= 0 {
		return 0
	}
	var squared float64
	for _, value := range clean {
		delta := value - mean
		squared += delta * delta
	}
	return math.Sqrt(squared/float64(len(clean))) / mean * 100
}
