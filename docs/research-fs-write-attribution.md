# Research: attributing file-system writes to processes

Question: can rerun (an unprivileged Go CLI) detect which of its own
children wrote a given file — to catch a target restarting itself by
writing files inside its own watch tree?

Researched 2026-11-01; all claims verified against primary sources by
a web-enabled runner.

## The short answer

No — not without privileges. No unprivileged API on any of our
platforms names the process behind a file event. rerun detects
self-write loops by signature instead (the loop guard, `target.go`).

## Linux

| Mechanism | Attribution | Catch |
|---|---|---|
| fanotify(7) | event carries the writer's PID | requires `CAP_SYS_ADMIN`; the unprivileged mode added in 5.13 explicitly hides the PID ("a user will also not receive the pid that generated the event") — man7.org `fanotify_init(2)` VERSIONS, `fanotify(7)` |
| eBPF (bpftrace) | yes | root |
| auditd | yes | root / `CAP_AUDIT_*` (man7.org `auditctl(8)`) |
| inotify(7) | no — event has no PID field | man7.org `inotify(7)` |
| fsnotify (Go) | no — and upstream won't add it | charmbracelet/fsnotify issue #593, closed "no support" |

Go wrappers: `s3rj1k/go-fanotify` exposes `EventMetadata.Pid` but the
repo is archived; `opcoder0/fanotify` is alive but exposes no PID. A
PID-capable wrapper would have to be written against the raw syscalls.

## macOS

| Mechanism | Attribution | Catch |
|---|---|---|
| Endpoint Security framework | yes — `es_process_t` on every event | requires the `com.apple.developer.endpoint-security.client` entitlement + a signed System Extension + user approval (developer.apple.com). Not viable for a CLI tool. |
| FSEvents / kqueue | no — paths only | |
| `fs_usage`, `opensnoop` (dtrace) | yes | root |

## Windows

ETW file providers carry process IDs; kernel-mode providers need
administrator.

## What rerun does instead

OS-level attribution being closed, the loop guard detects the
*signature*: `loopCycles` (4) consecutive runs shorter than
`loopRunWindow` (30s) whose trigger sets share a common file → one
`rerun: loop suspected` line naming the file with the `except:` line
to add, once per file per session. No suppression in v1 — the crash
backoff already throttles any loop to ≤1 restart per 10s.

For debugging ground truth on demand: `sudo fs_usage -w -f filesys`
(macOS) or `bpftrace` on `openat`/`write` (Linux, root).