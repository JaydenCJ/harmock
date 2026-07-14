// Package cli wires the harmock subcommands. Run is side-effect-free apart
// from its writers (and, for serve, the listening socket), so the whole CLI
// is exercisable in-process by tests.
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/JaydenCJ/harmock/internal/har"
	"github.com/JaydenCJ/harmock/internal/match"
	"github.com/JaydenCJ/harmock/internal/version"
)

// Exit codes, stable across releases.
const (
	ExitOK      = 0 // success
	ExitCheck   = 1 // `check` found error-level problems
	ExitUsage   = 2 // bad flags or arguments
	ExitRuntime = 3 // I/O or parse failure
)

const usage = `harmock %s — serve any HAR capture as a deterministic local mock API server

Usage:
  harmock serve  <capture.har> [flags]   start the mock server (127.0.0.1)
  harmock routes <capture.har> [flags]   list servable routes
  harmock show   <capture.har> --entry N print one entry in full
  harmock check  <capture.har> [flags]   lint the capture for serve-readiness
  harmock version                        print the version

Run 'harmock <command> -h' for command flags.
`

// Run dispatches args (without the program name) and returns an exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, usage, version.Version)
		return ExitUsage
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "routes":
		return cmdRoutes(args[1:], stdout, stderr)
	case "show":
		return cmdShow(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "harmock %s\n", version.Version)
		return ExitOK
	case "help", "--help", "-h":
		fmt.Fprintf(stdout, usage, version.Version)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "harmock: unknown command %q\n\n", args[0])
		fmt.Fprintf(stderr, usage, version.Version)
		return ExitUsage
	}
}

// parseArgs parses fs against args while allowing flags to appear after
// positional arguments (`harmock show capture.har --entry 2` reads
// naturally), returning the positionals in order.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// multiFlag collects repeatable string flags.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// loadRoutes parses the HAR at path and builds routes with the shared
// filtering flags. It is the common front half of every subcommand.
func loadRoutes(path string, hosts []string, stripPrefix string) (*har.Archive, []match.Route, []match.Skip, error) {
	a, err := har.ParseFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	routes, skips, err := match.BuildRoutes(a, match.BuildOptions{
		Hosts:       hosts,
		StripPrefix: stripPrefix,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return a, routes, skips, nil
}
