package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

type statusKey struct{ target, topic string }

type programStatus struct {
	out    io.Writer
	active map[statusKey]string
}

func (s *programStatus) set(target, topic, state, message string) {
	if s == nil {
		return
	}
	key := statusKey{target, topic}
	if s.active[key] == state {
		return
	}
	// Imported targets can contain slashes; protocol ID segments are limited to 32 ASCII bytes.
	hash := sha256.Sum256([]byte(target))
	id := fmt.Sprintf("rerun/%x/%s", hash[:12], topic)
	if state == "" {
		delete(s.active, key)
		fmt.Fprintf(s.out, "\x1b]7501;id=%s:state=clear\x1b\\", id)
		return
	}
	if s.active == nil {
		s.active = make(map[statusKey]string)
	}
	s.active[key] = state
	title := strings.ToValidUTF8(printable(target, false), "�")
	if len(title) > 192 {
		n := 192
		for !utf8.RuneStart(title[n]) {
			n--
		}
		title = title[:n]
	}
	enc := base64.StdEncoding.EncodeToString
	fmt.Fprintf(s.out, "\x1b]7501;id=%s:app=rerun:state=%s:title=%s:msg=%s\x1b\\", id, state, enc([]byte(title)), enc([]byte(message)))
}

func (s *programStatus) clearTarget(name string) {
	s.set(name, "build", "", "")
	s.set(name, "health", "", "")
}

func (s *programStatus) close() {
	if s != nil && len(s.active) > 0 {
		io.WriteString(s.out, "\x1b]7501;id=rerun:state=clear\x1b\\")
		clear(s.active)
	}
}

// Called on the model's event loop, not in a tea.Cmd: asynchronous writes could put an old error after its clear.
func (s *programStatus) observe(t *tuiTarget, ev event) {
	if s == nil || t.spec.Kind == composeKind {
		return
	}
	name := t.spec.Name
	switch ev.kind {
	case evBuildStarted:
		s.set(name, "build", "working", "Building")
	case evBuildFailed:
		s.set(name, "build", "error", "Build failed")
	case evBuildOK, evBuildCancelled:
		s.set(name, "build", "", "")
	case evHealth:
		if t.state != stateRunning || ev.runAt.Before(t.lastStart) {
			return
		}
		if ev.health == healthUnhealthy {
			s.set(name, "health", "error", "Health check failed")
		} else {
			s.set(name, "health", "", "")
		}
	case evStarted, evExited, evRunnerErr:
		s.set(name, "health", "", "")
	case evStopped:
		s.clearTarget(name)
	}
}
