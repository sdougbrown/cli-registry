package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// scopedConditions mirrors the condition set the manifests of a consumer like
// poo declare: every condition is a boolean the host knows at load.
var scopedConditions = map[string]string{
	"profile_side_effecting": "boolean",
	"messaging_enabled":      "boolean",
	"mode":                   "string",
	"limit":                  "number",
	"tags":                   "string[]",
	"ports":                  "number[]",
}

func TestParseAvailability(t *testing.T) {
	t.Run("types follow the declaration", func(t *testing.T) {
		got, err := parseAvailability([]string{
			"profile_side_effecting=true",
			"messaging_enabled=false",
			"mode=read-only",
			"limit=2.5",
			"tags=a, b",
			"ports=1,2",
		}, scopedConditions)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got["profile_side_effecting"] != true || got["messaging_enabled"] != false {
			t.Errorf("booleans = %v / %v", got["profile_side_effecting"], got["messaging_enabled"])
		}
		if got["mode"] != "read-only" {
			t.Errorf("string = %v", got["mode"])
		}
		if got["limit"] != 2.5 {
			t.Errorf("number = %v", got["limit"])
		}
		if tags, ok := got["tags"].([]any); !ok || len(tags) != 2 || tags[1] != "b" {
			t.Errorf("string[] = %#v", got["tags"])
		}
		if ports, ok := got["ports"].([]any); !ok || len(ports) != 2 || ports[0] != 1.0 {
			t.Errorf("number[] = %#v", got["ports"])
		}
	})

	t.Run("an empty array member list is an empty array", func(t *testing.T) {
		got, err := parseAvailability([]string{
			"profile_side_effecting=true", "messaging_enabled=true",
			"mode=x", "limit=0", "tags=", "ports=",
		}, scopedConditions)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if tags, ok := got["tags"].([]any); !ok || len(tags) != 0 {
			t.Errorf("tags = %#v", got["tags"])
		}
	})

	for _, tc := range []struct {
		name   string
		flags  []string
		want   string
		inputs bool
	}{
		{
			name:  "a declared condition must be supplied",
			flags: []string{"profile_side_effecting=true"},
			want:  "missing availability inputs: limit, messaging_enabled, mode, ports, tags",
		},
		{
			name: "an undeclared condition is refused",
			flags: []string{
				"profile_side_effecting=true", "messaging_enabled=true", "mode=x",
				"limit=1", "tags=a", "ports=1", "perm=read",
			},
			want: "no such condition",
		},
		{
			name:  "a flag without a value is refused",
			flags: []string{"profile_side_effecting"},
			want:  "want name=value",
		},
		{
			name: "a value that does not match its type is refused",
			flags: []string{
				"profile_side_effecting=sometimes", "messaging_enabled=true", "mode=x",
				"limit=1", "tags=a", "ports=1",
			},
			want: `"sometimes" is not a boolean`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAvailability(tc.flags, scopedConditions)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	t.Run("a registry without conditions takes no flags", func(t *testing.T) {
		if got, err := parseAvailability(nil, nil); err != nil || got != nil {
			t.Fatalf("got %v, %v", got, err)
		}
		if _, err := parseAvailability([]string{"mode=x"}, nil); err == nil {
			t.Fatal("expected an error for a flag a registry cannot consume")
		}
	})
}

func TestMCPAvailabilityFiltersTools(t *testing.T) {
	p := writeRawRegistry(t, scopedRegistry)
	st := newStore([]string{p}, map[string]any{"profile_side_effecting": false})
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
	if len(res.Tools) != 1 || res.Tools[0].Name != "gh-pr-view" {
		t.Fatalf("tools/list = %+v, want only the read-only tool", res.Tools)
	}

	call, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "gh-pr-edit"})
	if err == nil && !call.IsError {
		t.Fatalf("a withheld tool must not run: %+v", call)
	}
	_ = sess.Close()
}

func TestMCPHandlerRefusesToolTakenAwayBeforeRebind(t *testing.T) {
	p := writeRawRegistry(t, scopedRegistry)
	st := newStore([]string{p}, map[string]any{"profile_side_effecting": true})
	if _, err := st.reload(); err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "cli-registry", Version: "dev"}, nil)
	st.apply(srv)

	ctx := context.Background()
	_, sess := connect(t, srv)

	// The profile flips and the store reloads before the watcher rebinds: the
	// tool is still registered, so the handler has to refuse it itself.
	st.inputs = map[string]any{"profile_side_effecting": false}
	if changed, err := st.reload(); err != nil || !changed {
		t.Fatalf("reload: changed=%v err=%v", changed, err)
	}
	call, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "gh-pr-edit"})
	if err != nil {
		t.Fatal(err)
	}
	if !call.IsError {
		t.Fatalf("call should be refused: %+v", call)
	}
	if len(call.Content) == 0 {
		t.Fatal("refusal should carry the reason")
	}
	text, _ := call.Content[0].(*mcp.TextContent)
	if text == nil || !strings.Contains(text.Text, "the profile is read-only") {
		t.Fatalf("refusal should name the reason: %+v", call.Content[0])
	}
	_ = sess.Close()
}

func TestMCPReloadRemovesVanishedTools(t *testing.T) {
	p := writeRawRegistry(t, scopedRegistry)
	st := newStore([]string{p}, map[string]any{"profile_side_effecting": true})
	if _, err := st.reload(); err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "cli-registry", Version: "dev"}, nil)
	st.apply(srv)

	ctx := context.Background()
	_, sess := connect(t, srv)
	if res, err := sess.ListTools(ctx, &mcp.ListToolsParams{}); err != nil || len(res.Tools) != 2 {
		t.Fatalf("tools/list before the edit = %+v (%v)", res, err)
	}

	// The manifest drops one tool and its availability block: the tool set
	// must shrink to what the new manifests offer.
	kept := `{"version":1,"tools":{"gh-pr-view":{"description":"list pull requests",
		"command":["echo","list"],"timeout":"10s",
		"inputSchema":{"type":"object","properties":{},"additionalProperties":false},
		"output":{"mode":"raw"}}}}`
	if err := os.WriteFile(p, []byte(kept), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := st.reload(); err != nil || !changed {
		t.Fatalf("reload: changed=%v err=%v", changed, err)
	}
	st.apply(srv)

	res, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "gh-pr-view" {
		t.Fatalf("tools/list = %+v, want the vanished tool gone", res.Tools)
	}
	_ = sess.Close()
}

func TestRunRejectsMissingAvailabilityInput(t *testing.T) {
	p := writeRawRegistry(t, scopedRegistry)
	if got := run([]string{"-registry", p}); got != 1 {
		t.Fatalf("run without the declared condition: exit = %d, want 1", got)
	}
}
