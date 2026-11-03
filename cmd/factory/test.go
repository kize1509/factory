package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"factory/internal/harness"
)

// testCmd runs `factory test <verb>` and returns the exit code: 0 when the
// result passes, 1 when it fails, 2 for a usage or read error.
func testCmd(args []string) int {
	if len(args) < 1 || (args[0] != "replay" && args[0] != "results" && args[0] != "check") {
		fmt.Fprintln(os.Stderr, "usage: factory test replay <run> [--repo R --at COMMIT] [--json] | factory test results <run> [--json] | factory test check <repo> --feature F --technique static|tests [--commit C] [--cmd C] [--json]")
		return 2
	}
	verb := args[0]
	fs := flag.NewFlagSet("test "+verb, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	root := fs.String("root", ".", "the project folder that holds .factory/runs")
	repo := fs.String("repo", "", "replay: a git repo to replay on instead of the run's fixture")
	at := fs.String("at", "", "replay: the commit of --repo to replay at")
	feature := fs.String("feature", "", "check: the feature map entry")
	technique := fs.String("technique", "", "check: static or tests")
	commit := fs.String("commit", "", "check: the commit to check (default HEAD)")
	var cmds multi
	fs.Var(&cmds, "cmd", "check: a command to run instead of the technique's default (repeatable)")
	// the run id comes first, flags after it (or flags only before it)
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil || fs.NArg() > 0 {
			return 2
		}
		rest = rest[:1]
	}
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "need exactly one run id (check: one repo)")
		return 2
	}
	var res *harness.Result
	var err error
	switch verb {
	case "check":
		res, err = harness.Check(context.Background(), *root, rest[0], *feature, *technique, harness.CheckOptions{Commit: *commit, Commands: cmds})
	case "replay":
		res, err = harness.Replay(context.Background(), *root, rest[0], harness.ReplayOptions{Repo: *repo, At: *at})
	default:
		res, err = harness.Results(*root, rest[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *asJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	} else {
		printResult(res)
	}
	if !res.Pass {
		return 1
	}
	return 0
}

func printResult(r *harness.Result) {
	status := "PASS"
	if !r.Pass {
		status = "FAIL"
	}
	fmt.Printf("%s  %s\n", status, r.Run)
	for _, s := range r.Steps {
		mark := "ok  "
		if !s.Pass {
			mark = "FAIL"
		}
		fmt.Printf("  %s %s\n", mark, s.Name)
	}
	if f := r.Failure; f != nil {
		fmt.Printf("\nfailed at: %s\n  expected: %s\n  actual:   %s\n", f.Step, indent(f.Expected), indent(f.Actual))
	}
	fmt.Printf("\nreplay: %s\n", r.ReplayCmd)
}

// multi is a flag that can be given more than once.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, "; ") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n    ") }
