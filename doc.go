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
// # Public API (frozen at Stage 0 — see specs/manifest.md, specs/argv.md)
//
// This document records the frozen contract. Implementations land in their
// respective stages; the signatures below are normative.
//
//	type Registry                       // complete, immutable, thread-safe compiled registry
//	    Load(paths ...string) (*Registry, error)  // file | dir | repeated; sorted; all-or-nothing
//	    Tools() []Tool
//
//	type Tool                           // one compiled tool
//
//	type Options                        // per-call exec options: env, cwd, timeout, stdin mode
//
//	func (r *Registry) Call(ctx context.Context, tool Tool, args map[string]any, opt ...Options) (*Result, error)
//
//	type Result {
//	    Stdout, Stderr []byte
//	    Truncated      bool       // output hit the capture limit
//	    ExitCode       int
//	    Decoded        any        // per output.mode (raw | json | json-pointer)
//	    Duration       time.Duration
//	}
//
// # Typed errors
//
//	LoadError        — registry load / duplicate tool name
//	DefinitionError  — schema or argv binding that fails compilation
//	ValidationError  — args fail the input schema (carries []StructuralIssue)
//	ExecError        — process ran but exited outside success_exit_codes
//	TimeoutError     — process exceeded timeout (distinct from ExecError)
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
// A Registry is immutable after Load; Call snapshots the registry per call so
// a concurrent reload never lands mid-flight. Schemas are compiled once at load.
package cliregistry
