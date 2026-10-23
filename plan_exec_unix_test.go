//go:build unix

package cliregistry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A relative WithCWD is validated against the planner's working directory, so
// Plan must hand the host an absolute directory: a host executor with its own
// session base would otherwise resolve the same relative string into a
// directory Plan never allowed.
func TestAllowedRelativeCWDWorksFromAnotherExecutorBase(t *testing.T) {
	root, otherBase := t.TempDir(), t.TempDir()
	for _, base := range []string{root, otherBase} {
		if err := os.Mkdir(filepath.Join(base, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	r := fixtureRegistry(t, map[string]fixtureDef{"relative": probeTool(nil, fixtureDef{cwd: map[string]any{"mode": "allow", "roots": []any{root}}})})
	tool := mustTool(t, r, "relative")
	p, err := r.Plan(tool, map[string]any{}, WithCWD("sub"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p.Dir) {
		t.Fatalf("Plan.Dir = %q, want an absolute directory", p.Dir)
	}
	own, err := r.Call(context.Background(), tool, map[string]any{}, WithCWD("sub"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := p.ResolveEnv(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	// Model an executor process with a session base different from the planner's.
	argv := append([]string{"-c", `cd "$1" && shift && exec "$@"`, "host", p.Dir}, p.Argv...)
	cmd := exec.Command("/bin/sh", argv...)
	cmd.Dir, cmd.Env = otherBase, env
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	host, err := p.Finish(Output{Stdout: stdout})
	if err != nil {
		t.Fatal(err)
	}
	ownDir, hostDir := decodeProbe(t, own).Dir, decodeProbe(t, host).Dir
	if hostDir != ownDir {
		t.Fatalf("Plan.Dir=%q: Call cwd=%q, host executor cwd=%q (outside the only allowed root %q)", p.Dir, ownDir, hostDir, root)
	}
}

// A symlink inside an allowed root that points out of it is not inside the
// root: containment is checked on the resolved directory.
func TestSymlinkedCWDOutsideAllowedRootIsRejected(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := fixtureRegistry(t, map[string]fixtureDef{"t": probeTool(nil, fixtureDef{cwd: map[string]any{"mode": "allow", "roots": []any{root}}})})
	_, err := r.Plan(mustTool(t, r, "t"), map[string]any{}, WithCWD(link))
	if err == nil || !strings.Contains(err.Error(), "outside allowed roots") {
		t.Fatalf("Plan(WithCWD(%q)) = %v, want a containment error", link, err)
	}
	// A symlink that stays inside the root is fine and resolves to its target.
	inside := filepath.Join(root, "real")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	insideLink := filepath.Join(root, "alias")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Fatal(err)
	}
	p, err := r.Plan(mustTool(t, r, "t"), map[string]any{}, WithCWD(insideLink))
	if err != nil {
		t.Fatal(err)
	}
	if want, err := filepath.EvalSymlinks(inside); err != nil || p.Dir != want {
		t.Fatalf("Plan.Dir = %q, want the resolved %q (%v)", p.Dir, want, err)
	}
}
