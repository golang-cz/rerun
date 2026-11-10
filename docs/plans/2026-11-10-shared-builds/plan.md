# Shared builds: one build for targets that alias the same `build:`

Status: PLANNED

## Summary
Targets whose `build:` is a YAML alias of one anchor share a single build.
rerun runs that build once per trigger, then restarts every member that asked for it.
Today each target runs its own copy, so N targets sharing `go build -o bin/ ./cmd/...` start N identical builds at once.
On a big Go repo that is N × GOMAXPROCS compilers and can exhaust RAM on a cold cache.

## Config (rerun.yml)
```yaml
targets:
  omsx-api:
    build: &api_build make -C apps/api build-dev
    run: cd apps/api && ./bin/omsx
  bck-api:
    build: *api_build
    run: cd apps/api && ./bin/backoffice
  worker:
    build: *api_build
    run: cd apps/api && ./bin/worker
```
- The anchor name (`api_build`) is the share key. The anchor owner and every alias form one build group.
- Identical strings without an alias stay independent. Sharing is opt-in and visible in the file.
- Anchors are per document, so a group never spans imported files and always shares one `Dir`.
- No new keys. A config that uses no `build:` aliases behaves exactly as today.

## User flow
- Startup: all members start → one build → on success every member spawns; on failure every member shows `✗` and stays down.
- File change: members whose watcher fired within the debounce window join one build. Only they restart on success.
- Change during an in-flight group build: today's kill-and-union rule applies to the group; new requesters join the rerun.
- Failure: every requesting member shows `✗ build failed`, old runs keep going (today's rule per target).
- Each requesting member's pane shows the same `⚙` build rule and output. The list's build cell spins on all of them.
- Cancelling the build from any member cancels it for the whole group.
- Cooldown, backoff, and the loop guard stay per member and apply after the shared build finishes.

## Design
- `config.go`: while decoding targets, record `BuildKey` = anchor name when the `build` node is an anchor or an alias (`deref()` already resolves aliases). Empty key = private build, today's path.
- New `buildGroup` (one per non-empty key): owns the build process, the `tailBuffer`, the union of changed files, and the set of waiting members.
- `targetRunner.startBuild` with a group: register the member and its file set with the group instead of spawning. The group starts or restarts its build (kill-and-union at group level).
- `buildGroup.finish`: fan `evBuildOK`/`evBuildFailed` and the tail lines out to every waiting member, then each member runs its own `finishBuild` tail (`noteReplace`, `killCurrent`, `waitCooldown`, `spawn`).
- `evBuildStarted`/`evBuildOutput` fan out to waiting members, so the TUI needs no change.
- Loop guard: files written by the group build count as self-written for every member (`selfWritten`).
- Config reload (`↺`): a member whose `BuildKey` changed leaves its old group. A group with no members cancels its build.

## Steps
1. Parse `BuildKey` from anchors and aliases on `build:`.
   Verify: `config_test.go` table: anchor+aliases share a key; plain identical strings get none; anchors in an imported file do not leak.
2. Add `buildGroup` with register, kill-and-union, and fan-out finish; private builds keep the current code path.
   Verify: `build_test.go`: three members, one trigger → `StartCommand` called once, all three swap; a failure keeps all three old runs.
3. Join on change: a second member triggering mid-build joins the next run with unioned files.
   Verify: `build_test.go`: change for member B during A's build → one restart of the build, both swap once.
4. Startup: members launched together share one build.
   Verify: `target_test.go`: `rerun` with three members runs the build once before any spawn.
5. Loop guard and reload across groups.
   Verify: `build_leak_test.go`: group build writing a watched file pauses every member; reload moving a member out cancels nothing else.
6. Docs: README `build` row and a short "Shared builds" section with the example above.
   Verify: `go test ./...` green, README example parses in `config_test.go`.

## Out of scope
- Dedupe by identical command string without an alias.
- A global cap on concurrent builds across different groups.
- Dependencies between targets (`needs:`) or ordering of starts.
- Per-member binary change detection: a member restarts when it asked for the build, not when its output file changed.
