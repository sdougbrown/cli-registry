// Package cliregistry turns pre-registered CLI commands into typed tool calls.
//
// A "registered CLI" is a manifest describing a real command-line interface —
// argv, flags, and an input JSON Schema. cliregistry compiles that manifest
// into a standard tool descriptor, validates tool arguments against the
// schema before spawning, executes the command through a process engine, and
// decodes the result. The compiled descriptors are standard MCP JSON Schema,
// so any harness with a process runner can consume them (embedded directly, or
// through the bundled stdio/Streamable-HTTP MCP servers).
//
// # Public API
//
//	type Registry                       // complete, immutable, thread-safe compiled registry
//	    Load(paths ...string) (*Registry, error)  // file | dir | repeated; sorted; all-or-nothing
//	    Tools() []Tool
//	    Lookup(name string) (Tool, bool)
//
//	type Tool                           // one compiled tool
//
//	type Option                         // per-call options: WithCWD, WithTimeout, WithEnv
//
//	func (r *Registry) Call(ctx context.Context, tool Tool, args map[string]any, opt ...Option) (*Result, error)
//
//	type Result struct {
//	    Stdout, Stderr []byte
//	    Truncated      bool       // not the whole stream: the capture limit, a forced drain cut, or a failed capture read
//	    ExitCode       int
//	    Decoded        any        // per output.mode (raw | json | json-pointer)
//	    Duration       time.Duration
//	}
//
// # Planning and finishing a call yourself
//
// Call validates, builds and spawns in one step, which is all a caller that
// just wants the output needs. A host with its own executor — a sandbox, a
// scrubbed environment, a session working directory, its own cancellation and
// process reaping — uses Plan and Finish instead and runs the argv itself:
//
//	// WithCWD(dir) is for a tool whose manifest sets cwd.mode: allow; it is
//	// validated against the allowed roots and resolved to an absolute path.
//	p, err := reg.Plan(tool, args)
//	if err != nil { ... }                       // *ValidationError: nothing built
//	env, err := p.ResolveEnv(scrubbedLookup)    // resolve env:NAME against the host's view
//	if err != nil { ... }                       // a declared reference the host cannot resolve
//	dir := p.Dir
//	if dir == "" { dir = sessionDir }           // the manifest left it to the host
//	out, err := myExecutor(ctx, p.Argv, env, dir, p.Timeout)
//	if err != nil { ... }                       // my own cancel, timeout or start error
//	res, err := p.Finish(out)                   // same classification as Call
//
//	type Plan struct {
//	    Tool    string
//	    Argv    []string      // Argv[0] is the resolved command path
//	    Env     []EnvBinding  // the manifest's declared env, unresolved, name-sorted
//	    Dir     string        // set only when the manifest or WithCWD fixes it; "" = the host decides
//	    Timeout time.Duration // the manifest's, or WithTimeout's
//	}
//	type EnvBinding struct {
//	    Name      string
//	    Value     string // a literal, when Reference is false
//	    From      string // the host variable a manifest `env:NAME` refers to
//	    Reference bool   // true for an `env:NAME` reference; From may be empty
//	}
//	var BenignEnv []string        // variables Call copies from the host by default
//
//	func (r *Registry) Plan(tool Tool, args map[string]any, opt ...Option) (*Plan, error)
//	func (p *Plan) ResolveEnv(getenv func(string) (string, bool)) ([]string, error)
//	func (p *Plan) Finish(out Output) (*Result, error)
//
//	type Output struct {
//	    Stdout, Stderr []byte
//	    Truncated      bool
//	    ExitCode       int
//	    Duration       time.Duration
//	}
//
// Plan touches no process and reads no environment: an `env:NAME` reference
// stays unresolved in Plan.Env, so the host decides which environment a
// reference is resolved against. ResolveEnv applies BenignEnv, then the plan's
// bindings, then WithEnv overrides, using the lookup it is given.
//
// Plan.Dir is absolute with symlinks resolved whenever the manifest or WithCWD
// fixes it, so an executor with its own session base runs in the directory the
// plan validated; an empty Dir means the manifest left the choice to the host,
// which then picks its session directory. A fixed directory that cannot be
// fully resolved — it does not exist, is a dangling symlink, loops, or cannot
// be read — is an error from Plan, never an unresolved path handed to an
// executor.
//
// Finish applies the tool's success exit codes and output decoding to a
// captured run, so a host-side run is classified exactly as Call classifies
// it. It classifies a completed run and does not know why a process ended:
// when your executor cancels, times out or fails to start the command, return
// its own error before calling Finish. The same killed-process output is a
// *TimeoutError through Call and an *ExecError through Finish.
//
// A Plan comes from Registry.Plan; the zero value is not usable.
//
// # Process lifecycle (Call's engine)
//
//   - The child runs in its own process group with stdin closed and both
//     streams captured up to 1 MiB each.
//   - A cancelled or already-cancelled context stops the run immediately (the
//     process group on the Linux path; only the leader on the fallback
//     described below) and returns a *TimeoutError with Cause set to
//     ctx.Err(), the same error family v0.1.0 returned; IsTimeout is true for
//     it, and IsCanceled (or errors.Is against context.Canceled /
//     context.DeadlineExceeded) reports the context cause.
//   - The timeout stops the run the same way and returns a *TimeoutError with
//     a nil Cause (IsTimeout).
//   - Every exit on the Linux path kills the rest of the process group, so
//     same-group descendants do not outlive the call there, and the group is
//     signalled only while the leader's pid still names it.
//   - A descendant that holds the capture pipes does not turn a normal exit
//     into a timeout: the pipes are drained after the group kill, bounded at
//     500 ms (25 ms on cancellation). A drain cut at that bound, like a failed
//     capture read, sets Output.Truncated.
//
// These guarantees hold per process group and per platform. A descendant that
// leaves the group with setsid survives; its pipe is cut at the drain bound
// instead of being waited on. Windows has no process groups here — only the
// direct child is killed — so descendants survive. Only Linux observes the
// exit without reaping the leader, which is what keeps the group id valid. A
// path without that observation (every other Unix platform, and Linux when
// waitid fails) never sends a numeric group signal: timeout and cancellation
// stop only the leader through the runtime's guarded process operation, so
// same-group descendants may survive that path as well.
//
// # Typed errors
//
//	LoadError        — registry load / duplicate tool name
//	DefinitionError  — schema or argv binding that fails compilation
//	ValidationError  — args fail the input schema (carries []StructuralIssue)
//	ExecError        — process ran but exited outside success_exit_codes;
//	                   carries Stdout, Stderr and Truncated
//	TimeoutError     — the process was stopped before it finished (distinct
//	                   from ExecError): by the plan's timeout (nil Cause) or by
//	                   the caller's context (Cause = ctx.Err(), returned by
//	                   Unwrap). Carries the partial Stdout, Stderr and
//	                   Truncated, and the duration as Timeout (Timout is a
//	                   deprecated alias).
//
//	type StructuralIssue struct {
//	    Source string // "json-schema"
//	    Code   string
//	    Path   string // RFC 6901 pointer
//	}
//
// # Validator seam
//
//	type Validator interface {
//	    Validate(schema, value any) ([]StructuralIssue, error)
//	}
//
// The default is the built-in closed-vocabulary structural validator (Option B),
// matching the umpire-json-schema profile v1 supported subset. It is the only
// plugin point; everything else is closed.
//
// # Thread-safety
//
// A Registry is immutable after Load; Call and Plan snapshot the registry per
// call so a concurrent reload never lands mid-flight. Schemas are compiled
// once at load.
package cliregistry
