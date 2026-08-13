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
	var regs stringList
	fs := flag.NewFlagSet("cli-registry-mcp", flag.ContinueOnError)
	fs.StringVar(&transport, "transport", "stdio", "stdio or http")
	fs.StringVar(&addr, "addr", "127.0.0.1:9100", "listen address (http transport)")
	fs.StringVar(&token, "token", "", "bearer token for http (or CLI_REGISTRY_TOKEN)")
	fs.Var(&regs, "registry", "registry file or dir (repeatable)")
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

	store := newStore(paths)
	if err := store.reload(); err != nil {
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

// store holds the current immutable registry and rebinds the MCP tool set when
// the underlying manifests change.
type store struct {
	mu          sync.Mutex
	paths       []string
	reg         *cliregistry.Registry
	fingerprint string
}

func newStore(paths []string) *store { return &store{paths: paths} }

func (s *store) current() *cliregistry.Registry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reg
}

func (s *store) reload() error {
	reg, err := cliregistry.Load(s.paths...)
	if err != nil {
		return err
	}
	fp := fingerprint(reg)
	if s.reg != nil && fp == s.fingerprint {
		return nil // no change
	}
	s.mu.Lock()
	s.reg = reg
	s.fingerprint = fp
	s.mu.Unlock()
	return nil
}

func fingerprint(reg *cliregistry.Registry) string {
	tools := reg.Tools()
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	var b strings.Builder
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema)
		fmt.Fprintf(&b, "%s\u0000%s\u0000%s\u0000", t.Name, t.Description, schema)
	}
	return b.String()
}

// apply (re)writes the tool set on the server, notifying list_changed if there
// was a previous set.
func (s *store) apply(srv *mcp.Server) {
	reg := s.current()
	if reg == nil {
		return
	}
	for _, t := range reg.Tools() {
		name := t.Name
		desc := t.Description
		schema := t.InputSchema
		srv.AddTool(&mcp.Tool{Name: name, Description: desc, InputSchema: schema}, s.handler())
	}
}

// rebind swaps the tool set after a reload: removes tools no longer present and
// (re)adds current ones.
func (s *store) rebind(srv *mcp.Server) {
	old := s.current()
	if old == nil {
		s.apply(srv)
		return
	}
	var names []string
	for _, t := range old.Tools() {
		names = append(names, t.Name)
	}
	if len(names) > 0 {
		srv.RemoveTools(names...)
	}
	// remove/add via a clean apply: our fingerprinter auto-notifies on each
	// change; RemoveTools already emitted list_changed.
	s.apply(srv)
}

func (s *store) handler() mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := req.Params.Name
		reg := s.current()
		if reg == nil {
			return errResult(errors.New("registry not loaded")), nil
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
		if s.reload() == nil {
			s.rebind(srv)
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
