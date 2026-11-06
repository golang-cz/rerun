# Imports and target sources

## Summary

One rerun session can span several projects and non-process services:
- `imports:` pulls in another project's `rerun.yml`. Its targets run from that project's directory.
- An import can be optional: a missing file is skipped, and it loads later if it appears.
- A `compose:` import turns each docker-compose service into a target. rerun follows container state through Docker events, streams logs, and maps start/stop/restart to `docker compose`.
- The same "source" seam leaves room for a later `kubernetes:` import that follows a namespace.

## User-flow impact

- Left pane: each import becomes a section headed by its prefix (`bpn-api`, `infra`). Targets show as `bpn-api/api`.
- A missing optional import shows one dim row: `bpn-api (not found)`. It disappears once the file exists and loads.
- Compose targets look like local targets: state mark, `▶ started` / `✗ exited 137` / `↻ restarted` rules, health marks, columns.
- Compose targets have no file watching or builds. `r` restarts the container. Start/stop act on the container.
- Quitting rerun leaves compose containers running.
- Footer notices: `docker unreachable, retrying`, `import ../x/rerun.yml: <error>`.

## Config shape (rerun.yml)

```yaml
imports:
  - ../bpn-api/rerun.yml                 # required: missing file is a config error
  - rerun: ../wallet/rerun.yml
    optional: true                       # missing file is skipped
    as: wallet                           # prefix; default is the file's directory name
  - compose: ./docker-compose.yml
    optional: true
    as: infra                            # default is the compose project name
    group: backend                       # services join a local group, for shared columns
    services: [postgres, redis]          # default: all services

groups:
  backend:
    columns: ["level {.level}", "msg {.msg} {.error}"]

omsx-api:
  group: backend
  run: make -C apps/api run
```

- `imports` is a reserved top-level key, like `groups`.
- A bare string is the short form of `rerun: <path>`.
- Paths are relative to the importing file. `~` expands.
- Every import kind takes `optional`, `as` and `group`. `group` overrides the imported targets' own group.
- An imported file's groups are prefixed too: `bpn-api/backend`.
- Imports are one level deep. An `imports:` key inside an imported file is ignored, with a notice.
- The same absolute path imported twice loads once. A file that imports itself is an error.

## API shape (Go, cmd/rerun)

```go
// Config gains a source per target; Path stays the root file.
type Target struct {
	// ...existing fields
	Dir    string // base for run, build, health and on:; the defining file's dir
	File   string // file that defined the target; config writes go here
	Prefix string // "" for the root file
	Kind   string // "process" (default), "compose"
}

// What a targetRunner drives. The process backend wraps today's rerun.StartCommand code.
type backend interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	// Run streams state and output until ctx ends, emitting the existing events
	// (evStarted, evExited, evOutput, health) so the TUI needs no changes.
	Run(ctx context.Context, emit func(event)) error
}
```

- `targetRunner` keeps watch, build and restart policy. It delegates the process side to a `backend`.
- A compose target has no `on:`, `build:` or `health:` script. Health comes from Docker's own `health_status`.
- The config writer (`setColumns`) takes `Target.File` instead of `targetGroup.configPath`.

## Docker integration

Shell out to the `docker` CLI. Don't link the Docker SDK.
- It adds no dependency and respects the user's docker context (Colima, OrbStack, remote engines).
- `docker compose -f <file> config --format json` resolves services. Compose itself handles env interpolation, profiles and `extends`.
- One `docker events --format '{{json .}}' --filter label=com.docker.compose.project=<p>` stream per compose import. `Actor.Attributes["com.docker.compose.service"]` maps an event to a target.
- On start and after every reconnect, `docker compose ps --format json` reconciles state, because events sent during a disconnect are lost.
- Logs: `docker logs -f --since <last ts> <container>`. Re-attach on each `start` event.
- Actions: `docker compose up -d <svc>`, `stop <svc>`, `restart <svc>`. `up -d` creates a missing container, while `start` would fail.

| Docker event | rerun |
|---|---|
| `start` | running, `▶ started` |
| `die`, exitCode ≠ 0, not preceded by `stop`/`kill` from rerun or the CLI | crashed, `✗ exited <code>` |
| `die` after `stop`/`kill` | stopped |
| `oom` | crashed, label `oom` |
| `health_status: healthy/unhealthy` | existing health marks |
| `restart`, or `die` then `start` | `↻ restarted`; a crash loop stays visible |
| events stream ends | group shows `docker unreachable`; retry with backoff |

## Steps

1. **Per-target base dir.** Add `Target.Dir`, set to the defining file's directory. `run`, `build` and `health` commands run with `cmd.Dir = Target.Dir`. `on:` and `except:` resolve against it.
   Behavior change: today these follow the process cwd, which differs when rerun starts from a subdirectory (`LoadConfig` walks up).
   Verify: a test starts from a subdir and asserts cwd and watch paths. `make test`.
2. **Parse `imports:` for rerun files.** Handle the short and long forms, `optional`, `as`, `group`, and target and group prefixing. Dedupe by absolute path, and ignore nested imports with a notice. Errors point at the importing line with the usual source excerpt.
   Verify: config tests cover a required missing file (error), an optional missing file (skipped), a prefix clash, a duplicate import, a self-import and a nested import.
3. **Multi-file runtime.** `watchConfig` watches every imported file. For a missing optional file, it watches the nearest existing parent dir, so the file loads once it appears. The writer uses `Target.File`. The TUI shows a section per prefix, plus the `(not found)` row.
   Verify:
   - Reload tests cover editing an imported file, and creating a missing optional file.
   - A writer test saves columns for an imported target into the imported file.
4. **Extract `backend`.** Move process start/stop/restart/output out of `targetRunner` into `processBackend`. Make no behavior change.
   Verify: the full suite passes unchanged, and `go vet` is clean.
5. **Compose source.** Parse `compose:` imports, resolve services via `docker compose config`, and add `composeBackend` with the event mapping above.
   Verify:
   - Tests run against a fake `docker` script on `PATH` that replays canned `config`, `ps`, `events` and `logs` output. They cover crash vs stop, oom, health, reconnect catch-up and a crash loop.
   - Manual check: `docker compose kill postgres` in a real project shows `✗ exited 137`.
6. **Docs.** Add README sections for Imports and Docker Compose, and a SKILL.md entry. Add new sections; don't reword existing ones.
   Verify: the README examples parse in a config test.

Steps 1–3 ship as one PR, and steps 4–6 as a second.

## Later: Kubernetes

Out of scope for this plan. The config and API above leave room for it:

```yaml
imports:
  - kubernetes:
      context: dev
      namespace: wallet
      selector: app.kubernetes.io/part-of=wallet   # optional
    as: k8s-wallet
    optional: true                                 # context unreachable → skipped
```

- One target per workload (Deployment, StatefulSet, DaemonSet), not per pod. Pods come and go, but workloads stay stable.
- Shell out to `kubectl`, as with docker. That respects kubeconfig and auth plugins, and avoids `client-go`.
  - State: `kubectl get pods -w -o json` with the selector.
  - Logs: `kubectl logs -f --prefix -l <workload selector>`.
- Read-only first: logs and state only. A `rollout restart` action can follow behind a confirm prompt. Shared clusters make stop/scale risky.
- Workloads appear and disappear at runtime. The source needs a membership-change event that the TUI turns into rows.

## Decisions (suggested default, flip any)

1. Imports are required by default, and `optional: true` opts out. **Yes.**
2. Imported targets always get a prefix, even with no name clash. **Yes.**
3. Imports go one level deep. **Yes.**
4. Compose containers keep running when rerun quits. **Yes.**
5. Each import kind uses its own key (`rerun:`, `compose:`, later `kubernetes:`) instead of detection by file name. **Yes.**
6. Docker and Kubernetes go through their CLIs, not SDKs. **Yes.**
7. Step 1's cwd change applies to the root file too, not only to imports. **Yes.** The current subdirectory behavior is likely accidental.

## Out of scope

- Kubernetes, beyond the shape above.
- Watching files and rebuilding images for compose services. `docker compose watch` covers that.
- Nested imports.
- Remote imports by URL.
- Editing compose files from rerun.
