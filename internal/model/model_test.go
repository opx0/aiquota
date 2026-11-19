package model

import (
	"math"
	"testing"
)

func TestNormalizeWindow(t *testing.T) {
	for _, tc := range []struct {
		value float64
		valid bool
	}{{0, true}, {45, true}, {100, true}, {-1, false}, {101, false}, {math.NaN(), false}, {math.Inf(1), false}} {
		w := Window{UsedPercent: &tc.value}
		err := NormalizeWindow(&w)
		if (err == nil) != tc.valid {
			t.Fatalf("value %v: %v", tc.value, err)
		}
		if tc.valid && (w.RemainingPercent == nil || *w.RemainingPercent != 100-tc.value) {
			t.Fatalf("remaining for %v: %v", tc.value, w.RemainingPercent)
		}
	}
}

func TestNormalizeWindowRejectsInvalidAmounts(t *testing.T) {
	for _, v := range []float64{-1, math.NaN(), math.Inf(1)} {
		for _, w := range []Window{{UsedAmount: &v}, {RemainingAmount: &v}, {AllocationLimit: &v}} {
			if err := NormalizeWindow(&w); err == nil {
				t.Fatalf("accepted amount %v", v)
			}
		}
	}
}

func TestNormalizeWindowRejectsInconsistentPercentages(t *testing.T) {
	used, remaining := 25.0, 20.0
	w := Window{UsedPercent: &used, RemainingPercent: &remaining}
	if err := NormalizeWindow(&w); err == nil {
		t.Fatal("accepted inconsistent percentages")
	}
}
