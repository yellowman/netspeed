package measurement

import (
	"math"
	"reflect"
	"testing"
)

func closeEnough(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}

func TestPercentileUsesR7Interpolation(t *testing.T) {
	values := []float64{1, 2, 3, 4, 100}
	for percentile, want := range map[float64]float64{
		0: 1, 25: 2, 50: 3, 75: 4, 90: 61.6, 100: 100,
	} {
		if got := Percentile(values, percentile); !closeEnough(got, want) {
			t.Fatalf("Percentile(%v, %.0f) = %v; want %v", values, percentile, got, want)
		}
	}
}

func TestPrepareLatencyDropsWarmupAndFiltersOutlier(t *testing.T) {
	values := []float64{100, 80, 10, 11, 12, 13, 14, 2000}
	got := PrepareLatency(values, 2)
	want := []float64{10, 11, 12, 13, 14}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PrepareLatency = %#v; want %#v", got, want)
	}
}

func TestFilterIQRRetainsOriginalIfFilteringWouldBeDestructive(t *testing.T) {
	values := []float64{1, 100, 200, 300}
	got := FilterIQR(values)
	if len(got) < len(values)/2 {
		t.Fatalf("FilterIQR returned too few samples: %#v", got)
	}
}

func TestCleanJitterAndCV(t *testing.T) {
	values := []float64{math.NaN(), -1, 0, 10, 20, 30, math.Inf(1)}
	if got, want := Clean(values), []float64{10, 20, 30}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Clean = %#v; want %#v", got, want)
	}
	if got := Jitter([]float64{10, 20, 30}); !closeEnough(got, 8) {
		t.Fatalf("Jitter = %v; want 8", got)
	}
	if got := CoefficientOfVariation([]float64{10, 20, 30}); !closeEnough(got, 40.8248290463863) {
		t.Fatalf("CV = %v; want population CV", got)
	}
}
