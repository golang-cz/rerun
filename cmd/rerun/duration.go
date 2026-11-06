package main

import (
	"fmt"
	"time"
)

// fmtDur is the one duration format for the TUI and the event log.
// Whole seconds truncate, so 59.9s reads 59s instead of rounding into 1m.
func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return "0s"
	}
	secs := int(d / time.Second)
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 600:
		return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
	case secs < 3600:
		// Seconds past 10m are noise in a list row.
		return fmt.Sprintf("%dm", secs/60)
	case secs < 86400:
		return fmt.Sprintf("%dh%02dm", secs/3600, secs/60%60)
	}
	return fmt.Sprintf("%dd%dh", secs/86400, secs/3600%24)
}
