package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMedFn(t *testing.T) {
	cases := []struct {
		in  []time.Duration
		out time.Duration
	}{
		{nil, 0},
		{[]time.Duration{time.Second}, time.Second},
		{[]time.Duration{2 * time.Second, time.Second}, 1500 * time.Millisecond},
		{[]time.Duration{2 * time.Second, time.Second, 3 * time.Second}, 2 * time.Second},
		{[]time.Duration{2 * time.Second, time.Second, 3 * time.Second, 4 * time.Second}, 2500 * time.Millisecond},
	}

	for i, c := range cases {
		got := medFn(c.in)
		assert.Equal(t, c.out, got, "%d", i)
	}
}

func TestPctlFn(t *testing.T) {
	cases := []struct {
		in  []time.Duration
		pct int
		out time.Duration
	}{
		{nil, 50, 0},
		{[]time.Duration{time.Second}, 50, time.Second},
		{[]time.Duration{time.Second}, 90, time.Second},
		{[]time.Duration{time.Second}, 99, time.Second},
		{[]time.Duration{time.Second, 2 * time.Second}, 50, 1500 * time.Millisecond},
		{[]time.Duration{time.Second, 2 * time.Second}, 90, 2 * time.Second},
		{[]time.Duration{time.Second, 2 * time.Second}, 99, 2 * time.Second},
		{[]time.Duration{time.Second, 2 * time.Second, 3 * time.Second}, 50, 2 * time.Second},
		{[]time.Duration{time.Second, 2 * time.Second, 3 * time.Second}, 90, 3 * time.Second},
		{[]time.Duration{time.Second, 2 * time.Second, 3 * time.Second}, 10, time.Second},
		{[]time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}, 10, time.Second},
		{[]time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}, 50, 2500 * time.Millisecond},
		{[]time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}, 90, 4 * time.Second},
		{[]time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}, 99, 4 * time.Second},
	}

	for i, c := range cases {
		got := pctlFn(c.pct, c.in)
		assert.Equal(t, c.out, got, "%d", i)
	}
}
