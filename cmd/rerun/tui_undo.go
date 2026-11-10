package main

import "unicode"

// inputSnap is one input state: its text and cursor as a rune index.
type inputSnap struct {
	text string
	pos  int
}

// undoStack is one text input's edit history. Each undo step is the state before an edit.
type undoStack struct {
	undo, redo []inputSnap
	typing     bool      // the last edit typed one rune, so the next typed rune may join its word
	last       inputSnap // the state the last edit left
	lastRune   rune
}

// edit records a change from one state to another; typed is the rune a keypress added, 0 for a paste, delete or replace.
func (u *undoStack) edit(from, to inputSnap, typed rune) {
	// A space joins the word before it; a letter after a space starts a new step.
	joins := u.typing && typed != 0 && from == u.last && (unicode.IsSpace(typed) || !unicode.IsSpace(u.lastRune))
	if !joins {
		u.undo = append(u.undo, from)
	}
	u.redo = nil
	u.typing, u.last, u.lastRune = typed != 0, to, typed
}

// undoStep returns the state to restore for an undo, or false at the bottom of the stack.
func (u *undoStack) undoStep(cur inputSnap) (inputSnap, bool) {
	return u.move(cur, &u.undo, &u.redo)
}

// redoStep returns the state to restore for a redo, or false when nothing was undone.
func (u *undoStack) redoStep(cur inputSnap) (inputSnap, bool) {
	return u.move(cur, &u.redo, &u.undo)
}

// move pops one state off src and pushes cur onto dst; the next typed rune starts a new step.
func (u *undoStack) move(cur inputSnap, src, dst *[]inputSnap) (inputSnap, bool) {
	n := len(*src)
	if n == 0 {
		return cur, false
	}
	s := (*src)[n-1]
	*src = (*src)[:n-1]
	*dst = append(*dst, cur)
	u.typing = false
	return s, true
}

// historyKey reports undo or redo for a key name: alt+z undoes, alt+y and alt+shift+z redo.
func historyKey(key string) (undo, redo bool) {
	switch key {
	case "alt+z":
		return true, false
	case "alt+y", "alt+shift+z", "alt+Z", "alt+shift+Z":
		return false, true
	}
	return false, false
}

// typedRune is the rune a single-rune insert adds, or 0 for a paste.
func typedRune(s string) rune {
	if r := []rune(s); len(r) == 1 {
		return r[0]
	}
	return 0
}
