package cliregistry

// This file implements the registry surface: loading, enumeration, calling,
// options, and typed errors. The public contract (types, invariants,
// thread-safety) is documented in doc.go and specs/*.md.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sdougbrown/cli-registry/manifest"
	"github.com/sdougbrown/cli-registry/schema"
)

// StructuralIssue is re-exported from the schema package as the public issue
// type (doc.go contract).
type StructuralIssue = schema.StructuralIssue

// LoadError indicates the registry could not be loaded (discovery, decode,
// version, or duplicate-tool problems in the manifests).
type LoadError struct {
	Err error
}

func (e *LoadError) Error() string {
	if e.Err == nil {
		return "registry load failed"
	}
	return "registry load failed: " + e.Err.Error()
}
func (e *LoadError) Unwrap() error { return e.Err }

// DefinitionError indicates a tool body failed to compile — unsupported schema
// vocabulary, bad argv bindings, or an unresolvable command path.
type DefinitionError struct {
	Tool string
	Err  error
}

func (e *DefinitionError) Error() string {
	return fmt.Sprintf("tool %q definition: %v", e.Tool, e.Err)
}
func (e *DefinitionError) Unwrap() error { return e.Err }

// ValidationError indicates the call args were refused before anything was
// built: they failed the tool's input schema, or a value a binding would emit
// as a separate argv element would be read by the command as an option. No
// process was spawned.
type ValidationError struct {
	Tool   string
	Issues []StructuralIssue
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("tool %q validation failed (%d issue(s))", e.Tool, len(e.Issues))
}

// ExecError indicates the process ran to completion but exited outside the
// tool's success exit codes. It carries what the process wrote before it
// exited, so a host can show the output that came with the failure.
type ExecError struct {
	Tool      string
	Args      []string
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Truncated bool
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("tool %q exited %d: %s", e.Tool, e.ExitCode, string(e.Stderr))
}

// TimeoutError indicates the process was stopped before it finished and
// carries the partial output captured before the stop. It is returned in two
// cases:
//
//   - the plan's own timeout elapsed (Cause is nil);
//   - the caller's context ended the run (Cause is ctx.Err(), either
//     context.Canceled or context.DeadlineExceeded).
//
// A context-terminated run keeps this type because v0.1.0 reported it that
// way: an existing timeout branch keeps catching it, and IsCanceled (or
// errors.Is with the context error, through Unwrap) tells the two cases apart.
type TimeoutError struct {
	Tool string
	Args []string
	// Timeout is how long the process was allowed to run.
	Timeout time.Duration
	// Timout is a deprecated alias for Timeout, kept so callers that read or
	// set the misspelled field keep working. Both are set.
	Timout time.Duration
	// Cause is the context error that stopped the run, and nil when the
	// timeout did. Unwrap returns it.
	Cause     error
	Stdout    []byte
	Stderr    []byte
	Truncated bool
}

func (e *TimeoutError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("tool %q stopped: %v", e.Tool, e.Cause)
	}
	d := e.Timeout
	if d == 0 {
		d = e.Timout
	}
	return fmt.Sprintf("tool %q timed out after %s", e.Tool, d)
}

// Unwrap returns the context error that stopped the run, so
// errors.Is(err, context.Canceled) and errors.Is(err, context.DeadlineExceeded)
// report the caller's own cause.
func (e *TimeoutError) Unwrap() error { return e.Cause }

// Registry is a compiled, immutable, thread-safe set of tools.
type Registry struct {
	tools map[string]*tool
	order []string // deterministic tool name order
}

// Load reads every manifest reachable from paths and compiles the registry.
// Every tool must compile; a single bad tool fails the whole load
// (all-or-nothing).
func Load(paths ...string) (*Registry, error) {
	doc, err := manifest.Load(paths...)
	if err != nil {
		return nil, &LoadError{Err: err}
	}
	r := &Registry{tools: map[string]*tool{}}
	for name := range doc.Tools {
		if err := r.compileTool(doc, name); err != nil {
			return nil, &DefinitionError{Tool: name, Err: err}
		}
	}
	for name := range r.tools {
		r.order = append(r.order, name)
	}
	sort.Strings(r.order)
	return r, nil
}

// Tools returns the compiled tools in deterministic (name-sorted) order.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		out = append(out, Tool{
			Name:        t.name,
			Description: t.description,
			InputSchema: t.inputSchemaRaw,
		})
	}
	return out
}

// Lookup returns the compiled Tool for name, or (zero, false).
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	if !ok {
		return Tool{}, false
	}
	return Tool{Name: t.name, Description: t.description, InputSchema: t.inputSchemaRaw}, true
}

// Call validates args against tool's schema, builds argv, spawns the command,
// and returns the decoded result. Args that fail validation return a
// *ValidationError before any process is spawned.
//
// Call is Plan plus the library's own exec: a host that runs commands itself
// uses Plan and Finish instead, and sees the same classification.
func (r *Registry) Call(ctx context.Context, tool Tool, args map[string]any, opt ...Option) (*Result, error) {
	p, err := r.Plan(tool, args, opt...)
	if err != nil {
		return nil, err
	}
	return p.run(ctx)
}

// Result is the decoded outcome of a tool call.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	ExitCode  int
	Decoded   any
	Duration  time.Duration
}

// Option configures a single call.
type Option func(*callOptions)

type callOptions struct {
	cwd     string // requested cwd ("" = manifest-scoped/inherit)
	timeout time.Duration
	env     map[string]string // extra, overriding declared defaults
}

func defaultCallOptions() callOptions {
	return callOptions{env: map[string]string{}}
}

// WithCWD requests a working directory for the call. In "allow" cwd mode the
// requested path must fall within the tool's allowed roots, else the call
// fails. A tool without an allow scope leaves the working directory to the
// host, so a request for one is accepted as given. Either way Plan resolves it
// to an absolute path with symlinks resolved and reports it as Plan.Dir; a
// directory that cannot be fully resolved (missing, dangling symlink, loop or
// unreadable) is an error at plan time.
func WithCWD(dir string) Option { return func(o *callOptions) { o.cwd = dir } }

// WithTimeout overrides the tool's default timeout for a single call.
func WithTimeout(d time.Duration) Option { return func(o *callOptions) { o.timeout = d } }

// WithEnv adds or overrides declared environment variables for a single call.
func WithEnv(k, v string) Option { return func(o *callOptions) { o.env[k] = v } }

// IsTimeout reports whether err is a *TimeoutError (errors.As-aware helper). A
// context-terminated run is also a *TimeoutError, for v0.1.0 compatibility;
// use IsCanceled to tell it apart from the plan's own timeout.
func IsTimeout(err error) bool { var te *TimeoutError; return errors.As(err, &te) }

// IsCanceled reports whether err carries the end of the caller's context —
// context.Canceled or context.DeadlineExceeded — rather than the plan's own
// timeout. It follows the wrapped cause, so it works on any error that wraps a
// context error, not just on Call's own returns.
func IsCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
