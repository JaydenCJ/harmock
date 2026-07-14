// The read-only subcommands: routes, show, and check.
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/JaydenCJ/harmock/internal/render"
)

// cmdRoutes lists every servable route in the capture.
func cmdRoutes(args []string, stdout, stderr io.Writer) int {
	var hosts multiFlag
	var stripPrefix, format string
	fs := flag.NewFlagSet("routes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&hosts, "host", "serve only entries recorded against this host (repeatable)")
	fs.StringVar(&stripPrefix, "strip-prefix", "", "remove this leading path segment from recorded paths")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "harmock routes: exactly one <capture.har> argument is required")
		return ExitUsage
	}
	if format != "text" && format != "json" {
		fmt.Fprintf(stderr, "harmock routes: invalid --format %q (want text or json)\n", format)
		return ExitUsage
	}
	_, routes, _, err := loadRoutes(pos[0], hosts, stripPrefix)
	if err != nil {
		fmt.Fprintf(stderr, "harmock routes: %v\n", err)
		return ExitRuntime
	}
	if format == "json" {
		if err := render.RoutesJSON(stdout, routes); err != nil {
			fmt.Fprintf(stderr, "harmock routes: %v\n", err)
			return ExitRuntime
		}
		return ExitOK
	}
	render.RoutesText(stdout, routes)
	return ExitOK
}

// cmdShow prints one entry in full, addressed by index or request line.
func cmdShow(args []string, stdout, stderr io.Writer) int {
	var entry int
	var route string
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&entry, "entry", -1, "entry index as printed by 'harmock routes'")
	fs.StringVar(&route, "route", "", `request line, e.g. "GET /api/pets"`)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) != 1 || (entry < 0 && route == "") {
		fmt.Fprintln(stderr, "harmock show: usage: harmock show <capture.har> --entry N | --route \"GET /path\"")
		return ExitUsage
	}
	_, routes, _, err := loadRoutes(pos[0], nil, "")
	if err != nil {
		fmt.Fprintf(stderr, "harmock show: %v\n", err)
		return ExitRuntime
	}
	if entry >= 0 {
		if len(routes) == 0 {
			fmt.Fprintf(stderr, "harmock show: capture has no servable entries (run 'harmock check %s')\n", pos[0])
			return ExitRuntime
		}
		if entry >= len(routes) {
			fmt.Fprintf(stderr, "harmock show: entry %d out of range (0-%d)\n", entry, len(routes)-1)
			return ExitUsage
		}
		render.ShowText(stdout, routes[entry])
		return ExitOK
	}
	method, path, ok := strings.Cut(route, " ")
	if !ok {
		fmt.Fprintf(stderr, "harmock show: invalid --route %q (want \"METHOD /path\")\n", route)
		return ExitUsage
	}
	for _, r := range routes {
		if r.Method == strings.ToUpper(method) && r.Path == path {
			render.ShowText(stdout, r)
			return ExitOK
		}
	}
	fmt.Fprintf(stderr, "harmock show: no entry matches %q\n", route)
	return ExitRuntime
}

// cmdCheck lints the capture and gates on error-level findings.
func cmdCheck(args []string, stdout, stderr io.Writer) int {
	var hosts multiFlag
	var stripPrefix, format string
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&hosts, "host", "serve only entries recorded against this host (repeatable)")
	fs.StringVar(&stripPrefix, "strip-prefix", "", "remove this leading path segment from recorded paths")
	fs.StringVar(&format, "format", "text", "output format: text or json")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "harmock check: exactly one <capture.har> argument is required")
		return ExitUsage
	}
	if format != "text" && format != "json" {
		fmt.Fprintf(stderr, "harmock check: invalid --format %q (want text or json)\n", format)
		return ExitUsage
	}
	a, routes, skips, err := loadRoutes(pos[0], hosts, stripPrefix)
	if err != nil {
		fmt.Fprintf(stderr, "harmock check: %v\n", err)
		return ExitRuntime
	}
	rep := render.Check(a, routes, skips)
	if format == "json" {
		if err := render.CheckJSON(stdout, rep); err != nil {
			fmt.Fprintf(stderr, "harmock check: %v\n", err)
			return ExitRuntime
		}
	} else {
		render.CheckText(stdout, pos[0], rep)
	}
	if rep.Errors() {
		return ExitCheck
	}
	return ExitOK
}
