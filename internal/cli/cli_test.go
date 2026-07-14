// In-process CLI integration tests: every subcommand is driven through
// Run() with captured writers, against the shipped example capture and
// fabricated temp-dir captures. No sockets, no subprocesses.
package cli

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/harmock/internal/version"
)

// examplePath points at the committed demo capture.
var examplePath = filepath.Join("..", "..", "examples", "petstore.har")

// run executes the CLI in-process and returns exit code, stdout, stderr.
func run(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// writeHAR drops a capture into a temp dir and returns its path.
func writeHAR(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "capture.har")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVersionCommand(t *testing.T) {
	code, out, _ := run("version")
	if code != ExitOK || out != "harmock "+version.Version+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	for _, alias := range []string{"--version", "-v"} {
		code, out, _ := run(alias)
		if code != ExitOK || !strings.Contains(out, version.Version) {
			t.Fatalf("%s: code=%d out=%q", alias, code, out)
		}
	}
}

func TestUsageAndHelp(t *testing.T) {
	code, out, _ := run("help")
	if code != ExitOK || !strings.Contains(out, "harmock serve") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
	code, _, errOut := run()
	if code != ExitUsage || !strings.Contains(errOut, "Usage") {
		t.Fatalf("no args: code=%d stderr=%q", code, errOut)
	}
	code, _, errOut = run("frobnicate")
	if code != ExitUsage || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("unknown: code=%d stderr=%q", code, errOut)
	}
}

func TestRoutesTextOnExample(t *testing.T) {
	code, out, _ := run("routes", examplePath)
	if code != ExitOK {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"GET", "POST", "DELETE", "/api/pets?limit=2", "replay 1/3", "replay 3/3"} {
		if !strings.Contains(out, want) {
			t.Errorf("routes output missing %q:\n%s", want, out)
		}
	}
}

func TestRoutesJSONOnExample(t *testing.T) {
	// Note the flags after the positional: parseArgs must accept both
	// orders, unlike Go's default flag handling.
	code, out, _ := run("routes", examplePath, "--format", "json")
	if code != ExitOK {
		t.Fatalf("code=%d", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(rows) != 9 {
		t.Fatalf("want 9 routes, got %d", len(rows))
	}
}

func TestRoutesHostFilter(t *testing.T) {
	code, out, _ := run("routes", examplePath, "--host", "cdn.example.test")
	if code != ExitOK || !strings.Contains(out, "/analytics.js") || strings.Contains(out, "/api/pets") {
		t.Fatalf("host filter broken: code=%d out=%s", code, out)
	}
}

func TestRoutesErrorPaths(t *testing.T) {
	code, _, errOut := run("routes", filepath.Join(t.TempDir(), "nope.har"))
	if code != ExitRuntime || errOut == "" {
		t.Fatalf("missing file: code=%d stderr=%q", code, errOut)
	}
	if code, _, _ := run("routes", examplePath, "--format", "yaml"); code != ExitUsage {
		t.Fatalf("bad format: code=%d, want %d", code, ExitUsage)
	}
}

func TestShowByEntryAndByRoute(t *testing.T) {
	code, out, _ := run("show", examplePath, "--entry", "2")
	if code != ExitOK {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"POST /api/pets", `{"name":"Kuro","species":"cat"}`, "response 201"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q", want)
		}
	}
	// The same entry is addressable by its request line.
	code, out, _ = run("show", examplePath, "--route", "GET /api/pets/1")
	if code != ExitOK || !strings.Contains(out, `"name":"Momo"`) {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestShowErrorPaths(t *testing.T) {
	code, _, errOut := run("show", examplePath, "--entry", "99")
	if code != ExitUsage || !strings.Contains(errOut, "out of range") {
		t.Fatalf("out of range: code=%d stderr=%q", code, errOut)
	}
	if code, _, _ := run("show", examplePath); code != ExitUsage {
		t.Fatalf("no selector: code=%d, want %d", code, ExitUsage)
	}
	code, _, errOut = run("show", examplePath, "--route", "GET /nope")
	if code != ExitRuntime || !strings.Contains(errOut, "no entry matches") {
		t.Fatalf("unknown route: code=%d stderr=%q", code, errOut)
	}
}

func TestCheckExampleIsClean(t *testing.T) {
	code, out, _ := run("check", examplePath)
	if code != ExitOK {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "servable: 9") {
		t.Fatalf("summary wrong:\n%s", out)
	}
	if !strings.Contains(out, "recorded 3 times") {
		t.Fatalf("duplicate info missing:\n%s", out)
	}
	// --format json carries the same numbers, machine-readable.
	code, out, _ = run("check", examplePath, "--format", "json")
	if code != ExitOK {
		t.Fatalf("json format: code=%d", code)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if rep["entries"] != float64(9) || rep["servable"] != float64(9) {
		t.Fatalf("rep = %v", rep)
	}
}

func TestCheckBrokenCaptureExitsOne(t *testing.T) {
	p := writeHAR(t, `{"log":{"version":"1.2","entries":[
		{"startedDateTime":"2026-07-01T09:00:00Z","time":1,
		 "request":{"method":"GET","url":"http://api.example.test/img"},
		 "response":{"status":200,"content":{"size":4,"mimeType":"image/png",
		   "text":"!!bad!!","encoding":"base64"}}}]}}`)
	code, out, _ := run("check", p)
	if code != ExitCheck {
		t.Fatalf("code=%d, want %d\n%s", code, ExitCheck, out)
	}
	if !strings.Contains(out, "undecodable") {
		t.Fatalf("finding missing:\n%s", out)
	}
}

func TestCheckNotJSONIsRuntimeError(t *testing.T) {
	p := writeHAR(t, "<html>oops</html>")
	code, _, errOut := run("check", p)
	if code != ExitRuntime || !strings.Contains(errOut, "not a HAR file") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestServeRejectsInvalidFlagValues(t *testing.T) {
	cases := [][]string{
		{examplePath, "--strategy", "random"},
		{examplePath, "--latency", "soon"},
		{examplePath, "--fallback-status", "42"},
		{examplePath, "--match-body", "strict"},
	}
	for _, args := range cases {
		var stderr bytes.Buffer
		path, f, code := parseServe(args, &stderr)
		if code != ExitOK {
			t.Fatalf("parse %v failed unexpectedly: %d", args, code)
		}
		if _, code, err := buildHandler(path, f, func(string, ...any) {}); code != ExitUsage || err == nil {
			t.Errorf("%v: code=%d err=%v, want usage error", args[1:], code, err)
		}
	}
}

func TestServeArgAndCaptureErrors(t *testing.T) {
	var stderr bytes.Buffer
	if _, _, code := parseServe(nil, &stderr); code != ExitUsage {
		t.Fatalf("no args: code=%d", code)
	}
	if _, _, code := parseServe([]string{"a.har", "b.har"}, &stderr); code != ExitUsage {
		t.Fatalf("two args: code=%d", code)
	}
	// A missing capture is a runtime error, not a usage error.
	path, f, _ := parseServe([]string{filepath.Join(t.TempDir(), "nope.har")}, &stderr)
	if _, code, err := buildHandler(path, f, func(string, ...any) {}); code != ExitRuntime || err == nil {
		t.Fatalf("missing file: code=%d err=%v", code, err)
	}
	// So is a capture with nothing servable in it.
	p := writeHAR(t, `{"log":{"version":"1.2","entries":[]}}`)
	path, f, _ = parseServe([]string{p}, &stderr)
	_, code, err := buildHandler(path, f, func(string, ...any) {})
	if code != ExitRuntime || err == nil || !strings.Contains(err.Error(), "no servable entries") {
		t.Fatalf("empty capture: code=%d err=%v", code, err)
	}
}

// buildExampleHandler assembles the full serve pipeline for the example
// capture with extra flags, without opening a socket.
func buildExampleHandler(t *testing.T, extra ...string) *httptest.Server {
	t.Helper()
	var stderr bytes.Buffer
	path, f, code := parseServe(append([]string{examplePath}, extra...), &stderr)
	if code != ExitOK {
		t.Fatalf("parseServe: %d (%s)", code, stderr.String())
	}
	h, code, err := buildHandler(path, f, func(string, ...any) {})
	if err != nil {
		t.Fatalf("buildHandler: %d %v", code, err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestServePipelineEndToEnd(t *testing.T) {
	srv := buildExampleHandler(t)
	res, err := srv.Client().Get(srv.URL + "/api/pets?limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status=%d headers=%v", res.StatusCode, res.Header)
	}
	var body bytes.Buffer
	if _, err := body.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.String(), `"Momo"`) {
		t.Fatalf("body = %s", body.String())
	}
}

func TestServePipelineSequentialJobPolling(t *testing.T) {
	srv := buildExampleHandler(t)
	want := []string{"pending", "running", "done", "done"}
	for i, w := range want {
		res, err := srv.Client().Get(srv.URL + "/api/jobs/42")
		if err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		if _, err := body.ReadFrom(res.Body); err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if !strings.Contains(body.String(), w) {
			t.Fatalf("poll %d: got %s, want %q", i, body.String(), w)
		}
	}
}

func TestServePipelineStripPrefixAndHost(t *testing.T) {
	srv := buildExampleHandler(t, "--host", "api.example.test", "--strip-prefix", "/api")
	res, err := srv.Client().Get(srv.URL + "/pets/1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d (strip-prefix should expose /pets/1)", res.StatusCode)
	}
}

func TestParseLatencyValues(t *testing.T) {
	if mode, ms, err := parseLatency("none"); err != nil || string(mode) != "none" || ms != 0 {
		t.Fatalf("none: %v %v %v", mode, ms, err)
	}
	if mode, _, err := parseLatency("record"); err != nil || string(mode) != "record" {
		t.Fatalf("record: %v %v", mode, err)
	}
	if mode, ms, err := parseLatency("150"); err != nil || string(mode) != "fixed" || ms != 150 {
		t.Fatalf("150: %v %v %v", mode, ms, err)
	}
	for _, bad := range []string{"-5", "fast", "1.5", "150ms"} {
		if _, _, err := parseLatency(bad); err == nil {
			t.Errorf("parseLatency(%q) should fail", bad)
		}
	}
}
