// The serve subcommand: flag parsing, handler assembly, and the listener.
package cli

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/JaydenCJ/harmock/internal/match"
	"github.com/JaydenCJ/harmock/internal/server"
)

// serveFlags holds every parsed serve option.
type serveFlags struct {
	addr           string
	port           int
	hosts          multiFlag
	stripPrefix    string
	strategy       string
	ignoreQuery    multiFlag
	matchHeaders   multiFlag
	matchBody      string
	latency        string
	latencyMS      int
	fallbackStatus int
	cors           bool
	noAdmin        bool
	quiet          bool
}

// parseServe parses serve's flags plus the capture path.
func parseServe(args []string, stderr io.Writer) (string, *serveFlags, int) {
	var f serveFlags
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.addr, "addr", "127.0.0.1", "bind address (loopback by default; never exposed unless you say so)")
	fs.IntVar(&f.port, "port", 8080, "listen port (0 picks a free port)")
	fs.Var(&f.hosts, "host", "serve only entries recorded against this host (repeatable)")
	fs.StringVar(&f.stripPrefix, "strip-prefix", "", "remove this leading path segment from recorded paths")
	fs.StringVar(&f.strategy, "strategy", "sequential", "replay strategy for duplicate recordings: sequential, first, last")
	fs.Var(&f.ignoreQuery, "ignore-query", "query key excluded from matching, e.g. cache busters (repeatable)")
	fs.Var(&f.matchHeaders, "match-header", "request header that participates in matching (repeatable)")
	fs.StringVar(&f.matchBody, "match-body", "auto", "request-body matching: auto, always, never")
	fs.StringVar(&f.latency, "latency", "none", "response delay: none, record, or a fixed duration in ms (e.g. 150)")
	fs.IntVar(&f.fallbackStatus, "fallback-status", 404, "status code for unmatched requests")
	fs.BoolVar(&f.cors, "cors", false, "overwrite recorded CORS with permissive headers and answer preflights")
	fs.BoolVar(&f.noAdmin, "no-admin", false, "disable the /__harmock__/ admin endpoints")
	fs.BoolVar(&f.quiet, "quiet", false, "suppress per-request logging")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return "", nil, ExitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "harmock serve: exactly one <capture.har> argument is required")
		return "", nil, ExitUsage
	}
	return pos[0], &f, ExitOK
}

// buildHandler validates the enum flags and assembles the handler.
// Split out so tests can drive the exact serve pipeline without a socket.
func buildHandler(path string, f *serveFlags, logf func(string, ...any)) (*server.Handler, int, error) {
	strategy, ok := match.ParseStrategy(f.strategy)
	if !ok {
		return nil, ExitUsage, fmt.Errorf("invalid --strategy %q (want sequential, first, or last)", f.strategy)
	}
	bodyMode, ok := match.ParseBodyMode(f.matchBody)
	if !ok {
		return nil, ExitUsage, fmt.Errorf("invalid --match-body %q (want auto, always, or never)", f.matchBody)
	}
	latency, latencyMS, err := parseLatency(f.latency)
	if err != nil {
		return nil, ExitUsage, err
	}
	if f.fallbackStatus < 100 || f.fallbackStatus > 599 {
		return nil, ExitUsage, fmt.Errorf("invalid --fallback-status %d", f.fallbackStatus)
	}

	_, routes, skips, err := loadRoutes(path, f.hosts, f.stripPrefix)
	if err != nil {
		return nil, ExitRuntime, err
	}
	if len(routes) == 0 {
		return nil, ExitRuntime, fmt.Errorf("%s: no servable entries (run 'harmock check %s')", path, path)
	}
	for _, s := range skips {
		logf("skipping entry %d: %s", s.Index, s.Reason)
	}

	m := match.New(routes, match.Options{
		IgnoreQuery:  f.ignoreQuery,
		MatchHeaders: f.matchHeaders,
		MatchBody:    bodyMode,
	})
	h := server.New(m, server.Config{
		Strategy:       strategy,
		FallbackStatus: f.fallbackStatus,
		CORS:           f.cors,
		Latency:        latency,
		LatencyMS:      latencyMS,
		NoAdmin:        f.noAdmin,
		Logf:           logf,
	})
	return h, ExitOK, nil
}

// parseLatency maps the --latency flag onto a mode plus fixed milliseconds.
func parseLatency(s string) (server.LatencyMode, int, error) {
	switch s {
	case "none", "":
		return server.LatencyNone, 0, nil
	case "record":
		return server.LatencyRecord, 0, nil
	}
	var ms int
	if _, err := fmt.Sscanf(s, "%d", &ms); err != nil || ms < 0 || fmt.Sprintf("%d", ms) != s {
		return "", 0, fmt.Errorf("invalid --latency %q (want none, record, or milliseconds)", s)
	}
	return server.LatencyFixed, ms, nil
}

// cmdServe runs the mock server until the process is interrupted.
func cmdServe(args []string, stdout, stderr io.Writer) int {
	path, f, code := parseServe(args, stderr)
	if code != ExitOK {
		return code
	}
	logf := func(format string, a ...any) {
		if !f.quiet {
			fmt.Fprintf(stderr, format+"\n", a...)
		}
	}
	h, code, err := buildHandler(path, f, logf)
	if err != nil {
		fmt.Fprintf(stderr, "harmock serve: %v\n", err)
		return code
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", f.addr, f.port))
	if err != nil {
		fmt.Fprintf(stderr, "harmock serve: %v\n", err)
		return ExitRuntime
	}
	noun := "routes"
	if h.RouteCount() == 1 {
		noun = "route"
	}
	fmt.Fprintf(stdout, "harmock: serving %d %s from %s\n", h.RouteCount(), noun, path)
	fmt.Fprintf(stdout, "harmock: listening on http://%s (strategy=%s)\n", ln.Addr(), f.strategy)
	if !f.noAdmin {
		fmt.Fprintf(stdout, "harmock: admin at http://%s%shealth\n", ln.Addr(), server.AdminPrefix)
	}
	if err := http.Serve(ln, h); err != nil {
		fmt.Fprintf(stderr, "harmock serve: %v\n", err)
		return ExitRuntime
	}
	return ExitOK
}
