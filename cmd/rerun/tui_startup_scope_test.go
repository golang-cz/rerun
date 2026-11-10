package main

import "testing"

// Startup scopes the pane to the cursor's row, the way a cursor move does.
func TestStartupScopesPaneToCursorGroup(t *testing.T) {
	targets := []Target{schemaTarget("api", "backend"), schemaTarget("worker", "backend"), target("web")}
	m := newConfigTUI(targets, &Config{})
	addLines(m, "api", "api line")
	addLines(m, "worker", "worker line")
	addLines(m, "web", "web line")
	m.frame()

	r, ok := m.cursorRow()
	if !ok || !r.header || r.schema != "backend" {
		t.Fatalf("cursor row = %+v, want the backend header at startup", r)
	}
	if m.schemaSel != "backend" {
		t.Fatalf("schemaSel = %q at startup, want backend", m.schemaSel)
	}
	if want := []string{"api line", "worker line"}; !equalStrings(rawPane(m), want) {
		t.Fatalf("pane = %q, want only the backend targets %q", rawPane(m), want)
	}
}

// The startup scope is automatic, so it must not arm the solo guard; a user scope does.
func TestStartupScopeLeavesSoloGuardOff(t *testing.T) {
	m := newConfigTUI([]Target{target("web"), target("api")}, &Config{})
	if m.solo != 0 {
		t.Fatalf("solo = %d at startup, want the cursor's target 0", m.solo)
	}
	if m.group.soloGuard.Load() {
		t.Fatal("startup scope armed the solo guard; a +name reload would not start new targets")
	}
	m.applyScope(1, "")
	if !m.group.soloGuard.Load() {
		t.Fatal("a user scope change did not arm the solo guard")
	}
}
