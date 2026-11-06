package main

import (
	"testing"
	"time"
)

func TestFmtDur(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "<1s"},
		{0, "<1s"},
		{40 * time.Millisecond, "<1s"},
		{200 * time.Millisecond, "<1s"},
		{800 * time.Millisecond, "<1s"},
		{950 * time.Millisecond, "<1s"},
		{999 * time.Millisecond, "<1s"},
		{1 * time.Second, "1s"},
		{2400 * time.Millisecond, "2s"},
		{59 * time.Second, "59s"},
		{59900 * time.Millisecond, "59s"},
		{65 * time.Second, "1m05s"},
		{60 * time.Second, "1m00s"},
		{time.Hour, "1h00m"},
		{3600 * time.Second, "1h00m"},
		{time.Hour + 20*time.Minute + 15*time.Second, "1h20m"},
		{24 * time.Hour, "1d0h"},
		{86400 * time.Second, "1d0h"},
		{27*time.Hour + 30*time.Minute, "1d3h"},
		{99 * time.Hour, "4d3h"},
	} {
		if got := fmtDur(tc.d); got != tc.want {
			t.Errorf("fmtDur(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
