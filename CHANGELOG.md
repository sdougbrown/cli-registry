# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[semantic versioning](https://semver.org/).

## v0.1.1 — 2026-10-23

Lets a host run a registered CLI through its own executor instead of the
library's, and makes the library's own execution stop, reap and report.

### Added

- `Registry.Plan` and `Plan`: validate args and build the command (argv, env
  bindings, working directory, timeout) without touching a process or reading
  the host environment.
- `EnvBinding`, `Plan.Env` and `Plan.ResolveEnv`: manifest `env:NAME`
  references stay unresolved in the plan, so a host resolves them against its
  own (possibly scrubbed) environment.
- `BenignEnv`: the variables `Call` copies from the host by default.
- `Output` and `Plan.Finish`: classify a host-captured run exactly as `Call`
  does — success exit codes, output decoding, `ExecError`.
- `TimeoutError.Cause` and `TimeoutError.Unwrap`: a run stopped by the
  caller's context is still reported as `*TimeoutError`, with `Cause` set to
  `ctx.Err()` and `Unwrap` returning it, so `IsTimeout` keeps working next to
  `errors.Is(err, context.Canceled)` and
  `errors.Is(err, context.DeadlineExceeded)`.
- `IsCanceled(error)`: reports whether an error carries the end of the
  caller's context (`context.Canceled` or `context.DeadlineExceeded`), which is
  how a context-terminated run is told apart from the plan's own timeout now
  that both are `*TimeoutError`.
- `ExecError.Stdout` and `ExecError.Truncated`; `TimeoutError.Stdout`,
  `TimeoutError.Stderr` and `TimeoutError.Truncated`.
- `TimeoutError.Timeout`, the correctly spelled timeout duration.

### Changed

- `Registry.Call` is now `Plan` plus the library's own process engine; callers
  that do not plan observe the same behavior apart from the fixes below.
- After any exit the rest of the process group is killed, so same-group
  descendants no longer survive a normal exit.
- A descendant that holds the capture pipes no longer turns a normal exit into
  a timeout; the pipe drain is bounded instead. A drain cut at that bound, like
  a failed capture read, sets `Truncated`, so a short stream is never reported
  as complete output.
- A fixed working directory (`WithCWD`, or the manifest's first allowed root)
  is resolved to an absolute path with symlinks resolved when `Plan` validates
  it, and `Plan.Dir` and `Call` both use that path, so a host executor with a
  different session base runs in the directory that was validated. Containment
  is checked on the resolved directory. An empty `Plan.Dir` still means
  "the host decides".
- A cancelled call drains the capture with a 25 ms bound instead of the 500 ms
  a normal exit or a timeout gets, so cancellation returns promptly even when a
  descendant that left the process group still holds the pipes.
- A context that has ended — cancelled, or already past its deadline when the
  call starts — kills the process group and returns `*TimeoutError`, the type
  v0.1.0 returned, with `Cause` set to `ctx.Err()`; `IsTimeout` stays true for
  it and `IsCanceled` reports the context cause. An already-ended context
  returns without spawning a process.

### Fixed

- A bare `env:` binding is a missing-reference error again, as in v0.1.0,
  instead of becoming an empty literal. `EnvBinding.Reference` records that a
  binding is a reference, so an empty `From` can no longer mean "literal".
- A `style: space` array binding emitted its flag once before the element loop
  and again for every element (`--tag --tag one --tag two`); each element now
  emits `--flag <element>`, and an empty array emits nothing. `specs/argv.md`
  is updated to match.

### Deprecated

- `TimeoutError.Timout` — use `Timeout`. Both fields are set.
