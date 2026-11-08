# Imports and target sources

Status: DONE

## Summary

One rerun session can span several projects and non-process services:
- `imports:` pulls in another project's `rerun.yml`. Its targets run from that project's directory.
- An import can be optional: a missing file is skipped, and it loads later if it appears.
- Importing a docker-compose file turns each service into a target. rerun follows container state through Docker events, streams logs, and maps start/stop/restart to `docker compose`.
- Targets move under a `targets:` key, so top-level keys never collide with target names again.
- The same "source" seam leaves room for a later Kubernetes import that follows a namespace.

## User-flow impact

- Left pane: each import becomes a section headed by its `group` name (`bpn-api`, `devbox`). Targets show as `bpn-api/api`.
- A missing optional import shows one dim row: `bpn-api (not found)`. It disappears once the file exists and loads.
- Compose targets look like local targets: state mark, `▶ started` / `✗ exited 137` / `↻ restarted` rules, health marks, columns.
- Compose targets have no file watching or builds. `r` restarts the container. Start/stop act on the container.
- Quitting rerun leaves compose containers running.
- Footer notices: `docker unreachable, retrying`, `import ../x/rerun.yml: <error>`.

## Config shape (rerun.yml)

```yaml
imports:
  - path: ../bpn-api/rerun.yml
    group: bpn-api
    optional: true
    groups: [backend]                    # import only these groups of that file
  - path: ../devops/devbox/docker-compose.yml
    group: devbox
    optional: true
    services: [postgres, redis]          # compose only
    profiles: [debug]                    # compose only

groups:
  backend:
    columns: ["level {.level}", "msg {.msg} {.error}"]
  devbox:                                # applies to the compose import's services
    columns: ["msg {.msg}"]

targets:
  omsx-api:
    group: backend
    run: make -C apps/api run
```

### One import form

- Every import is a map with `path:`. There is no short form and no per-kind key.
- The file decides the kind. A top-level `services:` key means compose. Anything else is a rerun file.
- `group` names the import's section and target prefix. The default is the file's directory name for a rerun file, and the compose project name for a compose file.
- If the importing file defines a group with that name, its columns apply to the imported targets. That's how compose services get columns.
- Filters depend on the kind:
  - Rerun file: `groups` keeps only the listed groups of the imported file.
  - Compose file: `services` and `profiles` keep only the listed services and profiles. Profiles are compose's own grouping.
- Unknown keys are an error, e.g. `services:` on a rerun import.
- Paths are relative to the importing file. `~` expands.
- An imported rerun file's groups are prefixed: `bpn-api/backend`.
- Imports are one level deep. An `imports:` key inside an imported file is ignored, with a notice.
- The same absolute path imported twice loads once. A file that imports itself is an error.

### Top-level keys: `targets:`

Today targets are top-level keys, next to the reserved `groups`. Every new reserved key (`imports`, later `settings`) can collide with a project's target name.
- Targets move under `targets:`, the way compose puts services under `services:`.
- Top level then holds only rerun's own keys: `imports`, `groups`, `targets`. Any other top-level key is an error, so typos surface.
- Legacy files keep working.
  - A file with no `targets:` key is read the old way, with targets at the top level.
  - rerun shows one footer notice: `rerun.yml uses top-level targets; run rerun -migrate`.
- `rerun -migrate` rewrites the file in place through the existing yaml.v3 node writer. It keeps comments and key order.
- Kind detection stays clean: compose files have `services:`, and rerun files have `targets:`, or neither in legacy files.

## API shape (Go, cmd/rerun)

```go
// Config gains a source per target; Path stays the root file.
type Target struct {
	// ...existing fields
	Dir    string // base for run, build, health and on:; the defining file's dir
	File   string // file that defined the target; config writes go here
	Import string // import group name; "" for the root file
	Kind   string // "process" (default), "compose"
}

// What a targetRunner drives. The process backend wraps today's rerun.StartCommand code.
type backend interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	// Run emits the existing events (evStarted, evExited, evOutput, health) until ctx ends.
	Run(ctx context.Context, emit func(event)) error
}
```

- `targetRunner` keeps watch, build and restart policy. It delegates the process side to a `backend`.
- A compose target has no `on:`, `build:` or `health:` script. Health comes from Docker's own `health_status`.
- The config writer (`setColumns`) takes `Target.File` instead of `targetGroup.configPath`. It writes under `targets:` or at the top level, matching the file's form.

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
| `die`, exitCode ≠ 0, not preceded by `stop`/`kill` | crashed, `✗ exited <code>` |
| `die` after `stop`/`kill` | stopped |
| `oom` | crashed, label `oom` |
| `health_status: healthy/unhealthy` | existing health marks |
| `restart`, or `die` then `start` | `↻ restarted`; a crash loop stays visible |
| events stream ends | group shows `docker unreachable`; retry with backoff |

## Steps

1. **`targets:` key and `-migrate`.** Parse `targets:` and keep the legacy top-level form. Reject unknown top-level keys in the new form. Add the footer notice and `rerun -migrate`. The writer handles both forms.
   Verify: config tests cover both forms and an unknown top-level key. A migrate test checks that comments and order survive, and that the migrated file parses to the same targets.
2. **Per-target base dir.** Add `Target.Dir`, set to the defining file's directory. `run`, `build` and `health` commands run with `cmd.Dir = Target.Dir`. `on:` and `except:` resolve against it.
   Behavior change: today these follow the process cwd, which differs when rerun starts from a subdirectory (`LoadConfig` walks up).
   Verify: a test starts from a subdir and asserts cwd and watch paths. `make test`.
3. **Parse `imports:` for rerun files.** Handle `path`, `group`, `optional` and the `groups` filter. Detect the kind, and prefix targets and groups. Dedupe by absolute path, ignore nested imports with a notice, and reject keys that don't fit the kind. Errors point at the importing line with the usual source excerpt.
   Verify: config tests cover a required missing file (error), an optional missing file (skipped), a group clash, the `groups` filter, a duplicate import, a self-import and a nested import.
4. **Multi-file runtime.** `watchConfig` watches every imported file. For a missing optional file, it watches the nearest existing parent dir, so the file loads once it appears. The writer uses `Target.File`. The TUI shows a section per import, plus the `(not found)` row.
   Verify:
   - Reload tests cover editing an imported file, and creating a missing optional file.
   - A writer test saves columns for an imported target into the imported file.
5. **Extract `backend`.** Move process start/stop/restart/output out of `targetRunner` into `processBackend`. Make no behavior change.
   Verify: the full suite passes unchanged, and `go vet` is clean.
6. **Compose source.** Detect compose imports and resolve services via `docker compose config`, applying the `services` and `profiles` filters. Add `composeBackend` with the event mapping above.
   Verify:
   - Tests run against a fake `docker` script on `PATH` that replays canned `config`, `ps`, `events` and `logs` output. They cover crash vs stop, oom, health, reconnect catch-up, a crash loop and both filters.
   - Manual check: `docker compose kill postgres` in a real project shows `✗ exited 137`.
7. **Docs.** Add README sections for `targets:`, Imports and Docker Compose, and a SKILL.md entry. Add new sections; don't reword existing ones. Update the README examples to the `targets:` form.
   Verify: the README examples parse in a config test.

PRs:
1. Step 1, `targets:` and `-migrate`.
2. Steps 2–4, imports.
3. Steps 5–7, compose and docs.

## Later: Kubernetes

Out of scope for this plan. It fits the single import form, with a URL in place of a file path:

```yaml
imports:
  - path: kube://dev/wallet                         # kube://<context>/<namespace>
    group: k8s-wallet
    optional: true                                  # context unreachable → skipped
    selector: app.kubernetes.io/part-of=wallet      # kube only
```

- One target per workload (Deployment, StatefulSet, DaemonSet), not per pod. Pods come and go, but workloads stay stable.
- Shell out to `kubectl`, as with docker. That respects kubeconfig and auth plugins, and avoids `client-go`.
  - State: `kubectl get pods -w -o json` with the selector.
  - Logs: `kubectl logs -f --prefix -l <workload selector>`.
- Read-only first: logs and state only. A `rollout restart` action can follow behind a confirm prompt. Shared clusters make stop/scale risky.
- Workloads appear and disappear at runtime. The source needs a membership-change event that the TUI turns into rows.

## Decisions (suggested default, flip any)

1. Imports are required by default, and `optional: true` opts out. **Yes.**
2. Imported targets always get the import's group as a prefix, even with no name clash. **Yes.**
3. Imports go one level deep. **Yes.**
4. Compose containers keep running when rerun quits. **Yes.**
5. There's one import form, `- path:`, and the kind is detected from the file content (or a `kube://` URL). **Yes.**
6. Docker and Kubernetes go through their CLIs, not SDKs. **Yes.**
7. Step 2's cwd change applies to the root file too, not only to imports. **Yes.** The current subdirectory behavior is likely accidental.
8. Targets move under `targets:`. Legacy top-level files keep working, with a notice and `rerun -migrate`. **Yes.**

## Out of scope

- Kubernetes, beyond the shape above.
- Watching files and rebuilding images for compose services. `docker compose watch` covers that.
- Nested imports.
- Remote imports by URL, other than `kube://` later.
- Editing compose files from rerun.
- Removing the legacy top-level target form.
