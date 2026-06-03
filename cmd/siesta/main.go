// Command siesta scales a Kubernetes node pool to a desired count. It runs
// either as a single-shot CLI (--mode=cli) or as a Huawei FunctionGraph handler
// (--mode=fg). The schedule itself lives in an external cron (FG timer,
// EventBridge, ...); siesta just applies the desired state it is told.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/rahadiangg/siesta/internal/runner/cli"
	"github.com/rahadiangg/siesta/internal/runner/fg"

	// Register the available providers via their init().
	_ "github.com/rahadiangg/siesta/internal/scaler/huaweicce"
)

func main() {
	os.Exit(dispatch(os.Args[1:], os.Getenv))
}

// dispatch resolves the run mode and hands off to the matching runner. For fg
// mode it blocks in Serve and never returns.
func dispatch(args []string, getenv func(string) string) int {
	switch mode := resolveMode(args, getenv); mode {
	case "fg":
		fg.Serve(getenv)
		return 0 // Serve blocks; unreachable in practice
	case "cli":
		return cli.Main(args, getenv, os.Stdout, os.Stderr)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (want cli|fg)\n", mode)
		return 2
	}
}

// resolveMode picks the run mode with precedence:
//  1. --mode / -mode flag
//  2. SIESTA_MODE env var
//  3. fg when RUNTIME_API_ADDR is set (running inside FunctionGraph)
//  4. cli (default)
func resolveMode(args []string, getenv func(string) string) string {
	if m := modeFromFlag(args); m != "" {
		return m
	}
	if m := getenv("SIESTA_MODE"); m != "" {
		return m
	}
	if getenv("RUNTIME_API_ADDR") != "" {
		return "fg"
	}
	return "cli"
}

func modeFromFlag(args []string) string {
	for i, a := range args {
		switch {
		case a == "-mode" || a == "--mode":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(a, "--mode="):
			return strings.TrimPrefix(a, "--mode=")
		case strings.HasPrefix(a, "-mode="):
			return strings.TrimPrefix(a, "-mode=")
		}
	}
	return ""
}
