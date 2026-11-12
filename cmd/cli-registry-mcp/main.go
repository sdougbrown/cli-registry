// Command cli-registry-mcp exposes a cli-registry as an MCP server.
//
// Two transports:
//
//	stdio  — on-demand (a harness spawns it)
//	http   — Streamable HTTP daemon (Claude/opencode point url: at it)
//
// The daemon watches the registry and, on change, re-registers the tool set
// and emits notifications/tools/list_changed so connected clients re-enumerate
// without a restart. Each call snapshots the current registry for race safety.
//
// A manifest's availability rules decide which tools are offered. The consumer
// supplies the conditions it declares as -availability name=value; an entry
// the rules leave unavailable is not registered, so the model never sees it,
// and a call to it is refused. The reason is logged to stderr, which is the
// only place a client can learn why a tool is missing.
//
// Security (http): binds to a loopback addr by default, requires a bearer token
// (CLI_REGISTRY_TOKEN token or -token) when set, and enforces Host/Origin checks.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sdougbrown/cli-registry"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	var transport, addr, token string
	var regs, availabilityFlags stringList
	fs := flag.NewFlagSet("cli-registry-mcp", flag.ContinueOnError)
	fs.StringVar(&transport, "transport", "stdio", "stdio or http")
	fs.StringVar(&addr, "addr", "127.0.0.1:9100", "listen address (http transport)")
	fs.StringVar(&token, "token", "", "bearer token for http (or CLI_REGISTRY_TOKEN)")
	fs.Var(&regs, "registry", "registry file or dir (repeatable)")
	fs.Var(&availabilityFlags, "availability", "availability condition input name=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	paths := append([]string{}, regs...)
	if env := os.Getenv("CLI_REGISTRY"); env != "" {
		paths = append(paths, strings.Split(env, ":")...)
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "cli-registry-mcp: no registry paths (use -registry or CLI_REGISTRY)")
		return 1
	}
	if token == "" {
		token = os.Getenv("CLI_REGISTRY_TOKEN")
	}

	// The declared condition types live in the manifests, so they load once
	// before the flags can be typed.
	loaded, err := cliregistry.Load(paths...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cli-registry-mcp: load:", err)
		return 1
	}
	inputs, err := parseAvailability(availabilityFlags, loaded.AvailabilityInputs())
	if err != nil {
		fmt.Fprintln(os.Stderr, "cli-registry-mcp:", err)
		return 1
	}

	store := newStore(paths, inputs)
	if _, err := store.reload(); err != nil {
		fmt.Fprintln(os.Stderr, "cli-registry-mcp: load:", err)
		return 1
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "cli-registry", Version: "dev"}, nil)
	store.apply(srv)

	switch transport {
	case "stdio":
		if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, "cli-registry-mcp:", err)
			return 1
		}
	case "http":
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})
		go store.watch(srv)
		if err := http.ListenAndServe(addr, authHandler(limitRequestBody(handler), token)); err != nil {
			fmt.Fprintln(os.Stderr, "cli-registry-mcp:", err)
			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "cli-registry-mcp: unsupported transport %q\n", transport)
		return 1
	}
	return 0
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseAvailability types the -availability flags by the condition types the
// manifest declares. Every declared condition must be supplied: Umpire treats
// a missing input as an error, never as a default, and a silently defaulted
// condition would offer a tool the profile should not have.
func parseAvailability(flags []string, declared map[string]string) (map[string]any, error) {
	if len(declared) == 0 {
		if len(flags) > 0 {
			return nil, fmt.Errorf("registry declares no availability conditions, got -availability %s", strings.Join(flags, " "))
		}
		return nil, nil
	}
	inputs := make(map[string]any, len(declared))
	for _, flagValue := range flags {
		name, value, ok := strings.Cut(flagValue, "=")
		if !ok {
			return nil, fmt.Errorf("-availability %q: want name=value", flagValue)
		}
		typ, known := declared[name]
		if !known {
			return nil, fmt.Errorf("-availability %q: no such condition; declared: %s", name, declaredConditions(declared))
		}
		typed, err := parseConditionValue(typ, value)
		if err != nil {
			return nil, fmt.Errorf("-availability %s: %w", name, err)
		}
		inputs[name] = typed
	}
	var missing []string
	for name := range declared {
		if _, ok := inputs[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing availability inputs: %s; declared: %s", strings.Join(missing, ", "), declaredConditions(declared))
	}
	return inputs, nil
}

// parseConditionValue converts one flag value to the JSON value Umpire reads
// for a condition of the declared type. Array members are comma-separated.
func parseConditionValue(typ, value string) (any, error) {
	switch typ {
	case "boolean":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("%q is not a boolean", value)
		}
		return b, nil
	case "number":
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", value)
		}
		return n, nil
	case "string":
		return value, nil
	case "string[]":
		return splitArray(value, func(item string) (any, error) { return item, nil })
	case "number[]":
		return splitArray(value, func(item string) (any, error) {
			n, err := strconv.ParseFloat(item, 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", item)
			}
			return n, nil
		})
	default:
		return nil, fmt.Errorf("declared type %q is not a condition type", typ)
	}
}

func splitArray(value string, parse func(string) (any, error)) (any, error) {
	if value == "" {
		return []any{}, nil
	}
	items := strings.Split(value, ",")
	out := make([]any, len(items))
	for i, item := range items {
		typed, err := parse(strings.TrimSpace(item))
		if err != nil {
			return nil, err
		}
		out[i] = typed
	}
	return out, nil
}

func declaredConditions(declared map[string]string) string {
	names := make([]string, 0, len(declared))
	for name, typ := range declared {
		names = append(names, name+"="+typ)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// store holds the current immutable registry and rebinds the MCP tool set when
// the underlying manifests change.
type store struct {
	mu           sync.Mutex
	paths        []string
	inputs       map[string]any
	reg          *cliregistry.Registry
	availability map[string]cliregistry.ToolAvailability
	// applied is the tool set last registered on the server, so a rebind
	// removes exactly what it added.
	applied     []string
	fingerprint string
}

func newStore(paths []string, inputs map[string]any) *store {
	return &store{paths: paths, inputs: inputs}
}

func (s *store) current() (*cliregistry.Registry, map[string]cliregistry.ToolAvailability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reg, s.availability
}

// reload loads the manifests and evaluates availability. It reports whether
// the offered tool set changed, so an unchanged registry does not churn the
// tool list and its list_changed notification.
func (s *store) reload() (bool, error) {
	reg, err := cliregistry.Load(s.paths...)
	if err != nil {
		return false, err
	}
	availability, err := reg.Availability(s.inputs)
	if err != nil {
		return false, fmt.Errorf("availability: %w", err)
	}
	fp := fingerprint(reg, availability)
	if s.reg != nil && fp == s.fingerprint {
		return false, nil // no change
	}
	s.mu.Lock()
	s.reg = reg
	s.availability = availability
	s.fingerprint = fp
	s.mu.Unlock()
	logUnavailable(availability)
	return true, nil
}

// fingerprint covers what a client can see: the offered tools and their
// descriptors. An unavailable entry is absent from the hash, so a change that
// only takes a tool away still reads as a change.
func fingerprint(reg *cliregistry.Registry, availability map[string]cliregistry.ToolAvailability) string {
	tools := reg.Tools()
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	var b strings.Builder
	for _, t := range tools {
		if !availability[t.Name].Available {
			continue
		}
		schema, _ := json.Marshal(t.InputSchema)
		fmt.Fprintf(&b, "%s\u0000%s\u0000%s\u0000", t.Name, t.Description, schema)
	}
	return b.String()
}

// logUnavailable reports gated entries on stderr. tools/list has nowhere to
// carry a reason, so this is where an operator learns why a tool is missing.
func logUnavailable(availability map[string]cliregistry.ToolAvailability) {
	var names []string
	for name, a := range availability {
		if !a.Available {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "cli-registry-mcp: %s is unavailable: %s\n", name, availability[name].Reason)
	}
}

// apply replaces the server's tool set with the currently offered tools,
// notifying list_changed when there was a previous set.
func (s *store) apply(srv *mcp.Server) {
	reg, availability := s.current()
	if reg == nil {
		return
	}
	s.mu.Lock()
	previous := s.applied
	s.mu.Unlock()
	if len(previous) > 0 {
		srv.RemoveTools(previous...)
	}
	var applied []string
	for _, t := range reg.Tools() {
		if !availability[t.Name].Available {
			continue
		}
		srv.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}, s.handler())
		applied = append(applied, t.Name)
	}
	s.mu.Lock()
	s.applied = applied
	s.mu.Unlock()
}

func (s *store) handler() mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := req.Params.Name
		reg, availability := s.current()
		if reg == nil {
			return errResult(errors.New("registry not loaded")), nil
		}
		// A reload can take a tool away before the next rebind reaches the
		// server, so the gate is checked at call time too.
		if a, ok := availability[name]; ok && !a.Available {
			return errResult(fmt.Errorf("tool %q is unavailable: %s", name, a.Reason)), nil
		}
		tool, ok := reg.Lookup(name)
		if !ok {
			return errResult(fmt.Errorf("unknown tool %q", name)), nil
		}
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errResult(fmt.Errorf("decode arguments: %w", err)), nil
			}
		}
		res, err := reg.Call(ctx, tool, args)
		if err != nil {
			return errResult(err), nil
		}
		return okResult(res), nil
	}
}

func okResult(res *cliregistry.Result) *mcp.CallToolResult {
	sc := map[string]any{
		"exit_code": res.ExitCode,
		"duration":  res.Duration.String(),
		"truncated": res.Truncated,
	}
	body := string(res.Stdout)
	if res.Decoded != nil {
		sc["decoded"] = res.Decoded
	} else {
		sc["stdout"] = body
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: body}},
		StructuredContent: sc,
	}
}

func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		IsError: true,
	}
}

func (s *store) watch(srv *mcp.Server) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		changed, err := s.reload()
		if err != nil {
			// A manifest edited into an invalid state keeps the last good
			// tool set serving; say so instead of failing silently.
			fmt.Fprintln(os.Stderr, "cli-registry-mcp: reload:", err)
			continue
		}
		if changed {
			s.apply(srv)
		}
	}
}

const maxRequestBody = 1 << 20 // 1 MiB

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

// authHandler enforces Host/Origin checks and optional bearer auth. Modeled on
// avenor's internal/mcpserver.
func authHandler(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host) || !allowedOrigin(r.Header.Get("Origin")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if token != "" && !bearerMatches(r.Header.Get("Authorization"), token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedHost(host string) bool {
	// loopback hosts (default bind): strip port
	h := strings.ToLower(host)
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func allowedOrigin(origin string) bool {
	if origin == "" {
		return true // non-browser clients send no Origin
	}
	return allowedHost(originHost(origin))
}

func originHost(origin string) string {
	o := strings.ToLower(origin)
	if i := strings.Index(o, "://"); i >= 0 {
		o = o[i+3:]
	}
	if i := strings.LastIndex(o, ":"); i >= 0 && !strings.Contains(o[i:], "]") {
		o = o[:i]
	}
	return o
}

func bearerMatches(header, want string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(strings.ToLower(header), strings.ToLower(prefix)) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
