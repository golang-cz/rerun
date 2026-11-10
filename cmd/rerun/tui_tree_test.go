package main

import "testing"

// Tree rules on the list: ← folds an open header, ← on a closed header or a row steps to its
// enclosing header, → unfolds a closed header, and → on an open header or a row focuses the pane.

func TestLeftFoldsAnOpenHeader(t *testing.T) {
	m := nestedFixture(t)
	m.cursor = 1 // backend, open
	press(m, "left")
	if !m.foldState["omsx/backend"] || m.cursor != 0 {
		t.Fatalf("← on an open header: fold=%v cursor=%d, want backend folded and the cursor on its import, since the emptied sub-header leaves", m.foldState, m.cursor)
	}
}

func TestLeftOnAClosedSubHeaderStepsToItsImport(t *testing.T) {
	m := nestedFixture(t)
	m.cursor = 1
	press(m, "left") // fold backend
	press(m, "left") // closed header: step up
	if m.cursor != 0 || !m.foldState["omsx/backend"] {
		t.Fatalf("← on a closed header: cursor=%d fold=%v, want the omsx header and backend still folded", m.cursor, m.foldState)
	}
}

func TestLeftOnATargetStepsToItsEnclosingHeader(t *testing.T) {
	m := nestedFixture(t)
	m.cursor = 2 // api, under backend
	press(m, "left")
	if m.cursor != 1 {
		t.Fatalf("← on a target: cursor=%d, want 1, its backend header", m.cursor)
	}
}

func TestLeftOnATopLevelTargetStepsToTheAllHeader(t *testing.T) {
	m := newTestModel(t, schemaTarget("api", "backend"), target("lone"))
	m.cursor = 3 // lone, under the all header
	press(m, "left")
	if m.cursor != 2 {
		t.Fatalf("← on a root target: cursor=%d, want 2, the all header", m.cursor)
	}
}

func TestLeftOnAClosedTopLevelHeaderDoesNothing(t *testing.T) {
	m := nestedFixture(t)
	m.foldState = map[string]bool{"omsx/backend": true, "omsx/webapps": true, allKeyPrefix + "omsx": true}
	m.cursor = 0
	press(m, "left")
	if m.cursor != 0 || len(m.listRows()) != 2 || m.paneFocus {
		t.Fatalf("← on a closed top header: cursor=%d fold=%v paneFocus=%v, want no change", m.cursor, m.foldState, m.paneFocus)
	}
}

func TestRightUnfoldsAClosedHeader(t *testing.T) {
	m := nestedFixture(t)
	m.foldState = map[string]bool{"omsx/backend": true}
	m.cursor = 0 // the import header, whose subtree folds two members
	press(m, "right")
	if m.foldState["omsx/backend"] || m.paneFocus {
		t.Fatalf("→ on a closed header: fold=%v paneFocus=%v, want unfolded and the list kept", m.foldState, m.paneFocus)
	}
}

func TestRightOnAnOpenHeaderOrARowFocusesThePane(t *testing.T) {
	for _, cursor := range []int{1, 2} { // open backend header, api row
		m := nestedFixture(t)
		m.cursor = cursor
		press(m, "right")
		if !m.paneFocus {
			t.Fatalf("→ on row %d: paneFocus = false, want the log pane focused", cursor)
		}
	}
}

func TestPaneFocusKeepsColumnArrows(t *testing.T) {
	m := nestedFixture(t)
	m.foldState = map[string]bool{"omsx/backend": true}
	m.cursor = 1 // closed backend header
	m.paneFocus = true
	press(m, "right")
	if m.paneCol != 1 {
		t.Fatalf("→ with pane focus: paneCol=%d, want 1", m.paneCol)
	}
	press(m, "left")
	if m.paneCol != 0 || !m.paneFocus || !m.foldState["omsx/backend"] {
		t.Fatalf("← with pane focus: paneCol=%d paneFocus=%v fold=%v, want the column moved and the fold untouched",
			m.paneCol, m.paneFocus, m.foldState)
	}
}
