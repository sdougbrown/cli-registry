// Command cli-registry validates and invokes registered CLI tools.
//
//	cli-registry validate [-registry FILE]... [PATHS...]
//	cli-registry run [-registry FILE]... TOOL [ARGS_JSON]
//
// Registry paths come from repeatable -registry flags, the CLI_REGISTRY
// environment variable (colon-separated), or trailing positional arguments
// (validate only). run requires the tool name as its first positional and
// optional JSON args as the second.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sdougbrown/cli-registry"
	"github.com/sdougbrown/cli-registry/internal/app"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "validate":
		return cmdValidate(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "version":
		printVersion()
		return 0
	default:
		usage()
		return 1
	}
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseReg returns registry paths from -registry flags plus CLI_REGISTRY env,
// and the remaining positional args. Positional-as-path handling is left to
// each subcommand (validate treats them as paths; run treats them as tool/args).
func parseReg(flagset *flag.FlagSet, positional []string) ([]string, []string, error) {
	var regs stringList
	flagset.Var(&regs, "registry", "registry file or dir (repeatable)")
	if err := flagset.Parse(positional); err != nil {
		return nil, nil, err
	}
	paths := []string{}
	paths = append(paths, regs...)
	if env := os.Getenv("CLI_REGISTRY"); env != "" {
		paths = append(paths, strings.Split(env, ":")...)
	}
	return paths, flagset.Args(), nil
}

func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.Usage = func() {}
	paths, extra, err := parseReg(fs, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cli-registry validate:", err)
		return 1
	}
	paths = append(paths, extra...)
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "cli-registry validate: no registry paths (use -registry, CLI_REGISTRY, or positional paths)")
		return 1
	}
	_, err = cliregistry.Load(paths...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid registry: %v\n", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, "ok")
	return 0
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	paths, rest, err := parseReg(fs, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cli-registry run:", err)
		return 1
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "cli-registry run: missing TOOL")
		return 1
	}
	toolName := rest[0]
	argsJSON := "{}"
	if len(rest) > 1 {
		argsJSON = rest[1]
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "cli-registry run: no registry paths")
		return 1
	}
	reg, err := cliregistry.Load(paths...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli-registry run: load: %v\n", err)
		return 1
	}
	tool, ok := reg.Lookup(toolName)
	if !ok {
		fmt.Fprintf(os.Stderr, "cli-registry run: unknown tool %q\n", toolName)
		return 1
	}
	var toolArgs map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &toolArgs); err != nil {
		fmt.Fprintf(os.Stderr, "cli-registry run: bad ARGS_JSON: %v\n", err)
		return 1
	}
	res, err := reg.Call(context.Background(), tool, toolArgs)
	if err != nil {
		var ve *cliregistry.ValidationError
		if errors.As(err, &ve) {
			b, _ := json.MarshalIndent(ve.Issues, "", "  ")
			fmt.Fprintf(os.Stderr, "cli-registry run: validation failed:\n%s\n", b)
			return 2
		}
		fmt.Fprintf(os.Stderr, "cli-registry run: %v\n", err)
		return 1
	}
	out := map[string]any{
		"exit_code": res.ExitCode,
		"duration":  res.Duration.String(),
		"truncated": res.Truncated,
	}
	switch t := res.Decoded.(type) {
	case nil:
		out["stdout"] = string(res.Stdout)
	default:
		_ = t
		out["decoded"] = res.Decoded
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	if res.ExitCode != 0 {
		return res.ExitCode
	}
	return 0
}

func printVersion() {
	fmt.Printf("cli-registry %s (commit %s)\n", app.Version, app.Commit)
}

func usage() {
	fmt.Fprintln(os.Stderr, `cli-registry - declarative CLI -> tool pipeline

Usage:
  cli-registry validate [-registry FILE]... [PATHS...]
  cli-registry run [-registry FILE]... TOOL [ARGS_JSON]
  cli-registry version

Registry paths come from -registry flags, the CLI_REGISTRY env var, or paths.`)
}
