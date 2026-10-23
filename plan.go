// Package cliregistry turns pre-registered CLI commands into typed tool calls.
//
// Plan (this file) separates "what command would run" from "run it": a host
// that owns an executor — a sandbox, a scrubbed environment, a session working
// directory, its own cancellation and process reaping — builds a Plan, runs the
// argv itself, and hands the captured output back to Finish.
package cliregistry

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// BenignEnv lists the environment variables Call copies from the host by
// default, in order, before the manifest's declared env. A host that runs a
// plan itself should copy the same names out of its own (scrubbed)
// environment; Plan.Env holds only the manifest's declarations.
var BenignEnv = []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR"}

// EnvBinding is one manifest-declared environment variable, unresolved. Value
// is the literal a manifest declares; From names the host variable a manifest
// `env:NAME` reference points at. Exactly one of the two is set.
type EnvBinding struct {
	Name  string
	Value string
	From  string
}

// Plan is one validated, fully built command that has not been run. A Plan is
// produced by Registry.Plan; the zero value is not usable.
type Plan struct {
	Tool    string        // tool name
	Argv    []string      // Argv[0] is the resolved command path
	Env     []EnvBinding  // the manifest's declared env, unresolved, name-sorted
	Dir     string        // set only when the manifest or WithCWD fixes it; "" = the host decides
	Timeout time.Duration // the manifest's, or WithTimeout's

	t         *tool
	overrides map[string]string // WithEnv values, applied last by ResolveEnv
}

// Output is a completed run, as a host captured it. Stdout and Stderr are the
// bytes the process wrote (possibly truncated by the host), ExitCode is its
// exit status, and Duration is how long it ran.
type Output struct {
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	ExitCode  int
	Duration  time.Duration
}

// Plan validates args and builds the command without running it. A
// *ValidationError means nothing was built; it is returned before any argv or
// env work.
//
// Plan touches no process and reads no host environment: env:NAME references
// stay unresolved (Env[].From) so the host can resolve them against its own
// environment — which is how a host applies its env scrubbing.
func (r *Registry) Plan(tool Tool, args map[string]any, opt ...Option) (*Plan, error) {
	t, ok := r.tools[tool.Name]
	if !ok {
		return nil, &LoadError{Err: fmt.Errorf("unknown tool %q", tool.Name)}
	}
	opts := defaultCallOptions()
	for _, o := range opt {
		o(&opts)
	}

	// validate before building anything
	if issues := t.schema.Validate(args); len(issues) > 0 {
		return nil, &ValidationError{Tool: t.name, Issues: issues}
	}

	argv, err := t.buildArgv(args, &opts)
	if err != nil {
		return nil, &DefinitionError{Tool: t.name, Err: err}
	}
	dir, err := t.resolveDir(&opts)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(t.env))
	for name := range t.env {
		names = append(names, name)
	}
	sort.Strings(names)
	env := make([]EnvBinding, 0, len(names))
	for _, name := range names {
		v := t.env[name]
		if from, ok := strings.CutPrefix(v, "env:"); ok {
			env = append(env, EnvBinding{Name: name, From: from})
		} else {
			env = append(env, EnvBinding{Name: name, Value: v})
		}
	}

	timeout := t.timeout
	if opts.timeout > 0 {
		timeout = opts.timeout
	}

	return &Plan{
		Tool:      t.name,
		Argv:      append([]string(nil), argv...),
		Env:       env,
		Dir:       dir,
		Timeout:   timeout,
		t:         t,
		overrides: opts.env,
	}, nil
}

// ResolveEnv turns the plan's env bindings into "NAME=value" entries for an
// executor, using getenv to look host variables up. BenignEnv names are copied
// first, then the manifest's bindings, then WithEnv overrides. A declared
// env:NAME that getenv does not return is an error.
//
// A host passes its own lookup (typically over a scrubbed environment) rather
// than os.LookupEnv, so a manifest reference can never read a variable the
// host removed.
func (p *Plan) ResolveEnv(getenv func(string) (string, bool)) ([]string, error) {
	values := map[string]string{}
	var order []string
	set := func(name, value string) {
		if _, ok := values[name]; !ok {
			order = append(order, name)
		}
		values[name] = value
	}
	for _, name := range BenignEnv {
		if v, ok := getenv(name); ok {
			set(name, v)
		}
	}
	for _, b := range p.Env {
		if b.From != "" {
			v, ok := getenv(b.From)
			if !ok {
				return nil, fmt.Errorf("referenced env %q not present", b.From)
			}
			set(b.Name, v)
			continue
		}
		set(b.Name, b.Value)
	}
	extra := make([]string, 0, len(p.overrides))
	for name := range p.overrides {
		extra = append(extra, name)
	}
	sort.Strings(extra)
	for _, name := range extra {
		set(name, p.overrides[name])
	}
	out := make([]string, 0, len(order))
	for _, name := range order {
		out = append(out, name+"="+values[name])
	}
	return out, nil
}

// Finish classifies a completed run exactly as Call does: it checks the exit
// code against the tool's success set, fills in the Result, and decodes stdout
// per output.mode. A run outside the success set returns *ExecError.
func (p *Plan) Finish(out Output) (*Result, error) {
	res := &Result{
		Stdout:    out.Stdout,
		Stderr:    out.Stderr,
		Truncated: out.Truncated,
		ExitCode:  out.ExitCode,
		Duration:  out.Duration,
	}
	if !p.t.output.successExitCodes[out.ExitCode] {
		return nil, &ExecError{
			Tool:     p.Tool,
			Args:     p.Argv,
			ExitCode: out.ExitCode,
			Stderr:   out.Stderr,
		}
	}
	if err := p.t.decodeOutput(res); err != nil {
		return nil, fmt.Errorf("decode output: %w", err)
	}
	return res, nil
}
