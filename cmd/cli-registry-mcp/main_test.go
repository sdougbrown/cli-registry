package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeRegistry(t *testing.T, toolJSON string) string {
	t.Helper()
	return writeRawRegistry(t, `{"version":1,"tools":{`+toolJSON+`}}`)
}

func writeRawRegistry(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// scopedRegistry gates one tool on a condition the consumer supplies at load.
const scopedRegistry = `{
	"version": 1,
	"tools": {
		"gh-pr-view": {"description":"list pull requests","command":["echo","list"],"timeout":"10s",
			"inputSchema":{"type":"object","properties":{},"additionalProperties":false},"output":{"mode":"raw"}},
		"gh-pr-edit": {"description":"edit a pull request","command":["echo","edit"],"timeout":"10s",
			"inputSchema":{"type":"object","properties":{},"additionalProperties":false},"output":{"mode":"raw"}}
	},
	"availability": {
		"conditions": {"profile_side_effecting": {"type": "boolean"}},
		"rules": [
			{"type": "enabledWhen", "field": "gh-pr-edit",
			 "when": {"op": "cond", "condition": "profile_side_effecting"},
			 "reason": "the profile is read-only"}
		]
	}
}`

func sayTool() string {
	return `"say":{"description":"echo a message","command":["echo"],"timeout":"10s",
		"inputSchema":{"type":"object","properties":{"message":{"type":"string"}},"additionalProperties":false,"required":["message"]},
		"bindings":[{"property":"message","style":"positional"}],"output":{"mode":"raw"}}`
}

func connect(t *testing.T, srv *mcp.Server) (*mcp.Client, *mcp.ClientSession) {
	t.Helper()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "cli-registry-test", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	sess, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client, sess
}

func TestMCPToolListAndCall(t *testing.T) {
	p := writeRegistry(t, sayTool())
	st := newStore([]string{p}, nil, nil)
	if _, err := st.reload(); err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "cli-registry", Version: "dev"}, nil)
	st.apply(srv)

	ctx := context.Background()
	_, sess := connect(t, srv)

	res, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "say" {
		t.Fatalf("tools/list = %+v", res.Tools)
	}

	call, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "say",
		Arguments: map[string]any{"message": "hi from mcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if call.IsError {
		t.Fatalf("call marked error")
	}
	var wrote bool
	for _, c := range call.Content {
		if tc, ok := c.(*mcp.TextContent); ok && strings.Contains(tc.Text, "hi from mcp") {
			wrote = true
		}
	}
	if !wrote {
		t.Fatalf("call result missing text: %+v", call.Content)
	}
	if call.StructuredContent == nil {
		t.Fatal("expected structuredContent")
	}
	_ = sess.Close()
}

func TestMCPListChangedOnReload(t *testing.T) {
	var fired atomic.Bool
	client := mcp.NewClient(&mcp.Implementation{Name: "cli-registry-test", Version: "0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { fired.Store(true) },
	})

	p := writeRegistry(t, sayTool())
	st := newStore([]string{p}, nil, nil)
	if _, err := st.reload(); err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "cli-registry", Version: "dev"}, nil)
	st.apply(srv)

	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	sess, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Now a second tool appears; reload + apply must emit list_changed.
	nowTool := `"now":{"description":"timestamp","command":["date","+%s"],"timeout":"10s",
		"inputSchema":{"type":"object","properties":{},"additionalProperties":false},"output":{"mode":"raw"}}`
	content := `{"version":1,"tools":{` + sayTool() + `,` + nowTool + `}}`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.reload(); err != nil || !changed {
		t.Fatalf("reload after adding a tool: changed=%v err=%v", changed, err)
	}
	st.apply(srv)

	// A reload that changes nothing must not churn the tool set: re-applying
	// every watch tick would re-notify list_changed on an unchanged registry.
	if changed, err := st.reload(); err != nil || changed {
		t.Fatalf("unchanged reload: changed=%v err=%v", changed, err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for !fired.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !fired.Load() {
		t.Fatal("expected tools/list_changed notification after reload")
	}
	_ = sess.Close()
}

func TestAuthHandler(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	srv := httptest.NewServer(authHandler(limitRequestBody(inner), "s3cret"))
	defer srv.Close()

	resp := func(token, host string) int {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			// host mismatch / loopback only: connection refused or rejected
			return 0
		}
		r.Body.Close()
		return r.StatusCode
	}
	// good token on loopback -> ok
	if got := resp("s3cret", ""); got != 200 {
		t.Fatalf("good token got %d", got)
	}
	// bad token -> 401
	if got := resp("wrong", ""); got != 401 {
		t.Fatalf("bad token got %d, want 401", got)
	}
	// no token -> 401
	if got := resp("", ""); got != 401 {
		t.Fatalf("no token got %d, want 401", got)
	}
}
