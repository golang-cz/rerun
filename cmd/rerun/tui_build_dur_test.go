package main

import (
	"testing"
	"time"
)

func TestBuildDurIsWholeSeconds(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{200 * time.Millisecond, "1s"},
		{1400 * time.Millisecond, "2s"},
		{59200 * time.Millisecond, "1m00s"},
		{65 * time.Second, "1m05s"},
	} {
		if got := buildDur(tc.d); got != tc.want {
			t.Errorf("buildDur(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
