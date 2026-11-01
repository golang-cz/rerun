package main

import "testing"

func TestNestGuardRefusesWhenRerunIsSet(t *testing.T) {
	t.Setenv("RERUN", "1")
	if got := nestGuard(); got != 1 {
		t.Fatalf("nestGuard() = %d, want 1 when RERUN is set", got)
	}
}

func TestNestGuardAllowsWhenRerunIsUnset(t *testing.T) {
	t.Setenv("RERUN", "")
	if got := nestGuard(); got != 0 {
		t.Fatalf("nestGuard() = %d, want 0 when RERUN is unset", got)
	}
}
