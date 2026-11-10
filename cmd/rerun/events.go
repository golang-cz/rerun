package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/golang-cz/rerun"
)

// eventKind is what a target runner just did.
type eventKind int

const (
	evOutput         eventKind = iota // one line of the target's own output
	evChanged                         // a file-change batch restarted the target
	evStarting                        // queued behind the start stagger, not spawned yet
	evStarted                         // a new generation is live
	evExited                          // a generation ended on its own
	evBackoff                         // the next restart waits out a crash-loop backoff
	evWatcherErr                      // the file watcher failed
	evRunnerErr                       // rerun could not start a generation
	evCleared                         // the screen clear the ad-hoc mode prints
	evReconcile                       // a rerun.yml reload changed the target list
	evNotice                          // rerun's own line about the config; err is set when a reload failed
	evConfigErr                       // a rerun.yml reload did not load; err is the parser's full error
	evConfigOK                        // a rerun.yml reload parsed, so any earlier config error is over
	evLoop                            // a target looks like it is rewriting its own trigger file
	evBuildStarted                    // a build began; the old run keeps going meanwhile
	evBuildFailed                     // a build failed; the old run, if any, is untouched
	evBuildOK                         // a build passed; the swap to a new run follows
	evHealth                          // the health probe changed state
	evBuildCancelled                  // a build was dropped and no new build follows
	evBuildOutput                     // one line of a build's own output, streamed as it arrives
	evStopped                         // a compose service went down because someone stopped it
)

// event is everything a target runner reports. The front-ends render it: the
// plain sink as prefixed text, the TUI as target rows and stream lines.
type event struct {
	kind   eventKind
	target string
	line   string
	count  int
	first  string
	status string
	code   int
	err    error
	at     time.Time
	// delay is the wait behind evBackoff, and ordinal the restart number in
	// the window that triggered it.
	delay   time.Duration
	ordinal int
	// change is the whole batch behind evChanged, which the TUI keeps so the
	// row can show the files it last reacted to.
	change rerun.ChangeSet
	// restarts is true when a spawn follows the batch, so the feed can name the target.
	restarts bool
	// cfg is the reconciled target list behind evReconcile.
	cfg *configChange

	// files are the changed paths behind evStarted, evBuildStarted/Failed and evLoop;
	// empty on a first or manual start. They reuse change's keys, sorted.
	files []string
	// dur is the build's length on evBuildOK/evBuildFailed, and sinceStart on evHealth.
	dur time.Duration
	// buildDur is the build behind an evStarted, 0 when the target has no build.
	buildDur time.Duration
	// prevUp is how long the run an evStarted replaced had been up, 0 if none.
	prevUp time.Duration
	// restart is true on evStarted when it replaced a run, and on evExited when
	// rerun itself stopped the run to swap it.
	restart bool
	// lines is the tail of a failed build's output, at most buildTailLines.
	lines []string
	// buildAt is the start of the build an evBuildOutput line came from.
	buildAt time.Time
	// owner is the member whose events a shared build's line is logged from; with at, it names the run.
	owner string
	// cmd and names are a shared build's command and the targets it names, for its one line.
	cmd   string
	names []string
	// health is the new state behind evHealth.
	health healthState
	// runAt is the start time of the run an evHealth belongs to, matching that run's evStarted.at.
	runAt time.Time
}

// configChange is a freshly reconciled target list. The TUI adopts it
// wholesale, because a reload can add, drop, and reorder rows.
//
// targets, runners, and stopped are index-parallel.
type configChange struct {
	targets []Target
	runners []*targetRunner
	// stopped is true for a target that must not be running: a target the command line left out
	// in the config, a target the user stopped, or one added under a solo.
	stopped []bool
	// missing is the optional imports whose file is absent, shown as dim rows.
	missing []missingImport
	// queries are the reloaded config's global saved queries.
	queries map[string]string
}

// sink is the front-end a target runner writes its events to.
type sink interface {
	emit(event)
}

// plainSink writes the prefixed plain text rerun printed before the TUI: the
// exact bytes non-TTY output and every existing test depend on.
type plainSink struct {
	out   io.Writer
	lines *sync.Mutex
}

func newPlainSink(out io.Writer) *plainSink {
	return &plainSink{out: out, lines: &sync.Mutex{}}
}

func (s *plainSink) emit(ev event) {
	s.lines.Lock()
	defer s.lines.Unlock()
	fmt.Fprint(s.out, s.render(ev))
}

// writer is the sink's direct writer, used by the ad-hoc path that passes the
// child's output through untouched.
func (s *plainSink) writer() io.Writer { return s.out }

func (s *plainSink) render(ev event) string {
	raw := ev.target == ""
	switch ev.kind {
	case evOutput:
		if raw {
			return ev.line + "\n"
		}
		return fmt.Sprintf("[%s] %s\n", ev.target, ev.line)

	case evChanged:
		// An error batch keeps the plain path's watcher-error line.
		if ev.change.Error != nil {
			if raw {
				return fmt.Sprintf("%v\n", ev.change.Error)
			}
			return fmt.Sprintf("[%s] watcher error: %v\n", ev.target, ev.change.Error)
		}
		plural := ""
		if ev.count > 1 {
			plural = "s"
		}
		if raw {
			return fmt.Sprintf("%s%s# %d file%s changed (e.g. %s)%s\n", clearTerminal, greenColor, ev.count, plural, ev.first, resetColor)
		}
		return fmt.Sprintf("[%s] %d file%s changed (e.g. %s)\n", ev.target, ev.count, plural, ev.first)

	case evExited:
		if ev.restart {
			return fmt.Sprintf("[%s] stopped for restart\n", ev.target)
		}
		if raw {
			return fmt.Sprintf("rerun: exited (%s) — waiting for changes\n", ev.status)
		}
		return fmt.Sprintf("[%s] exited (%s) — waiting for changes\n", ev.target, ev.status)

	case evWatcherErr:
		if raw {
			return fmt.Sprintf("%v\n", ev.err)
		}
		return fmt.Sprintf("[%s] watcher error: %v\n", ev.target, ev.err)

	case evRunnerErr:
		return fmt.Sprintf("rerun: %s%v\n", s.label(ev.target), ev.err)

	case evLoop:
		return fmt.Sprintf("rerun: loop suspected — %s paused after %d× re-triggered by %s; add it to except, or change another file to resume\n", ev.target, loopCycles, ev.line)

	case evBuildStarted:
		if joined(ev) {
			return ""
		}
		if len(ev.names) > 1 {
			return fmt.Sprintf("⚙ %s · %s\n", ev.cmd, strings.Join(ev.names, ", "))
		}
		return fmt.Sprintf("[%s] building (%s)\n", ev.target, fileCount(len(ev.files)))

	case evBuildOK:
		if joined(ev) {
			return ""
		}
		if len(ev.names) > 1 {
			return fmt.Sprintf("⚙ %s built %s · %s\n", ev.cmd, fmtDur(ev.dur), strings.Join(ev.names, ", "))
		}
		return fmt.Sprintf("[%s] build ok (%s)\n", ev.target, fmtDur(ev.dur))

	case evBuildFailed:
		if joined(ev) {
			return ""
		}
		var b strings.Builder
		if len(ev.names) > 1 {
			fmt.Fprintf(&b, "✗ %s build failed after %s: %v · %s\n", ev.cmd, fmtDur(ev.dur), ev.err, strings.Join(ev.names, ", "))
		} else {
			fmt.Fprintf(&b, "[%s] build failed after %s: %v\n", ev.target, fmtDur(ev.dur), ev.err)
		}
		for _, l := range ev.lines {
			fmt.Fprintf(&b, "[%s] build: %s\n", ev.target, l)
		}
		return b.String()

	case evHealth:
		return fmt.Sprintf("[%s] health: %s (%s after start)\n", ev.target, ev.health, fmtDur(ev.dur))

	case evStopped:
		return fmt.Sprintf("[%s] stopped\n", ev.target)

	case evCleared:
		if raw {
			return clearTerminal + "\n"
		}

	case evReconcile, evNotice, evConfigErr:
		// rerun's own line about the config, never a target's output. A reload
		// with nothing to report prints nothing.
		if ev.line == "" {
			return ""
		}
		return ev.line + "\n"
	}

	return ""
}

func fileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	if n == 0 {
		return "no files"
	}
	return fmt.Sprintf("%d files", n)
}

// label is the target's identity in rerun's own messages.
// joined reports whether ev is a joined target's copy of its owner's shared build, which the owner's line already logs.
func joined(ev event) bool {
	return ev.owner != "" && ev.owner != ev.target
}

func (s *plainSink) label(target string) string {
	if target == "" {
		return ""
	}
	return target + ": "
}
