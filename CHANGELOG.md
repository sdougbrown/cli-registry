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
- `CanceledError` and `IsCanceled`: a cancelled context kills the process
  group promptly and wraps `ctx.Err()`.
- `ExecError.Stdout` and `ExecError.Truncated`; `TimeoutError.Stdout`,
  `TimeoutError.Stderr` and `TimeoutError.Truncated`; the same fields on
  `CanceledError`.
- `TimeoutError.Timeout`, the correctly spelled timeout duration.

### Changed

- `Registry.Call` is now `Plan` plus the library's own process engine; callers
  that do not plan observe the same behavior apart from the fixes below.
- After any exit the rest of the process group is killed, so same-group
  descendants no longer survive a normal exit.
- A descendant that holds the capture pipes no longer turns a normal exit into
  a timeout; the pipe drain is bounded instead.
- An already-cancelled context returns without spawning.

### Fixed

- A `style: space` array binding emitted its flag once before the element loop
  and again for every element (`--tag --tag one --tag two`); each element now
  emits `--flag <element>`, and an empty array emits nothing. `specs/argv.md`
  is updated to match.

### Deprecated

- `TimeoutError.Timout` — use `Timeout`. Both fields are set.
