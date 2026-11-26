package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// programStatus mirrors each row's build and health cells to the terminal as
// OSC 7501 program status. It holds no state of its own beyond what it last
// reported: sync derives every record from the rows.
type programStatus struct {
	out    io.Writer
	active map[statusKey]statusReport
}

type statusKey struct{ target, topic string }

type statusReport struct{ state, msg string }

var (
	reportBuilding    = statusReport{"working", "Building"}
	reportBuildFailed = statusReport{"error", "Build failed"}
	reportUnhealthy   = statusReport{"error", "Health check failed"}
)

// rowReports is what a row's cells say. A compose row's health is its
// container's, not the service's, and a stopped row reports nothing.
// The zero report means none.
func rowReports(t *tuiTarget) (build, health statusReport) {
	if t.spec.Kind == composeKind || t.state == stateStopped {
		return
	}
	switch {
	case t.building:
		build = reportBuilding
	case t.buildFailed:
		build = reportBuildFailed
	}
	if t.runStarted && t.state.up() && t.health == healthUnhealthy {
		health = reportUnhealthy
	}
	return
}

// sync reports every record that changed since the last call and clears the
// ones no row holds any more, removed rows included. It runs on the model's
// event loop, not in a tea.Cmd: asynchronous writes could put an old error after its clear.
func (s *programStatus) sync(targets []*tuiTarget) {
	if s == nil {
		return
	}
	want := map[statusKey]statusReport{}
	for _, t := range targets {
		add := func(topic string, r statusReport) {
			if r != (statusReport{}) {
				want[statusKey{t.spec.Name, topic}] = r
			}
		}
		build, health := rowReports(t)
		add("build", build)
		add("health", health)
	}
	byKey := func(a, b statusKey) int {
		return cmp.Or(cmp.Compare(a.target, b.target), cmp.Compare(a.topic, b.topic))
	}
	for _, k := range slices.SortedFunc(maps.Keys(s.active), byKey) {
		if _, ok := want[k]; !ok {
			fmt.Fprintf(s.out, "\x1b]7501;id=%s:state=clear\x1b\\", statusID(k))
		}
	}
	enc := base64.StdEncoding.EncodeToString
	for _, k := range slices.SortedFunc(maps.Keys(want), byKey) {
		if r := want[k]; s.active[k] != r {
			fmt.Fprintf(s.out, "\x1b]7501;id=%s:app=rerun:state=%s:title=%s:msg=%s\x1b\\",
				statusID(k), r.state, enc([]byte(statusTitle(k.target))), enc([]byte(r.msg)))
		}
	}
	s.active = want
}

// close clears every record rerun still holds, on quit.
func (s *programStatus) close() {
	if s != nil && len(s.active) > 0 {
		io.WriteString(s.out, "\x1b]7501;id=rerun:state=clear\x1b\\")
		clear(s.active)
	}
}

// statusID hashes the target name: imported names can contain slashes, and an
// ID segment is limited to 32 ASCII bytes.
func statusID(k statusKey) string {
	hash := sha256.Sum256([]byte(k.target))
	return fmt.Sprintf("rerun/%x/%s", hash[:12], k.topic)
}

// statusTitle is the target name as printable UTF-8 of at most 192 bytes.
func statusTitle(name string) string {
	title := strings.ToValidUTF8(printable(name, false), "�")
	if len(title) <= 192 {
		return title
	}
	n := 192
	for !utf8.RuneStart(title[n]) {
		n--
	}
	return title[:n]
}
