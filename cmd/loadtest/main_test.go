package main

import (
	"testing"
	"time"
)

func TestPercentileUsesNearestRank(t *testing.T) {
	values := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for _, test := range []struct {
		percentile int
		want       time.Duration
	}{{50, 5}, {95, 10}, {99, 10}, {100, 10}} {
		if got := percentile(values, test.percentile); got != test.want {
			t.Errorf("percentile %d = %v, want %v", test.percentile, got, test.want)
		}
	}
}
