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
