// Tests for terminal/JSON rendering and the check linter. Rendering must
// be deterministic: same routes in, same bytes out.
package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JaydenCJ/harmock/internal/har"
	"github.com/JaydenCJ/harmock/internal/match"
)

// entry is a test factory for a servable HAR entry.
func entry(method, rawurl string, status int, body string) har.Entry {
	return har.Entry{
		StartedDateTime: "2026-07-01T09:00:00Z",
		Time:            10,
		Request:         har.Request{Method: method, URL: rawurl},
		Response: har.Response{
			Status:  status,
			Content: har.Content{Size: int64(len(body)), MimeType: "application/json", Text: body},
		},
	}
}

func buildAll(t *testing.T, entries ...har.Entry) (*har.Archive, []match.Route, []match.Skip) {
	t.Helper()
	a := &har.Archive{Log: har.Log{Version: "1.2", Entries: entries}}
	routes, skips, err := match.BuildRoutes(a, match.BuildOptions{})
	if err != nil {
		t.Fatalf("BuildRoutes: %v", err)
	}
	return a, routes, skips
}

func TestRoutesTextHeaderAndRows(t *testing.T) {
	_, routes, _ := buildAll(t,
		entry("GET", "http://api.example.test/api/pets?limit=2", 200, `{"pets":[]}`),
		entry("POST", "http://api.example.test/api/pets", 201, "{}"),
	)
	var buf bytes.Buffer
	RoutesText(&buf, routes)
	out := buf.String()
	for _, want := range []string{"METHOD", "GET", "POST", "/api/pets?limit=2", "200", "201"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Rows with an empty NOTE column must not carry trailing whitespace —
	// the README quotes this table verbatim.
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.TrimRight(l, " ") != l {
			t.Errorf("trailing whitespace in line %q", l)
		}
	}
	// Same input must render byte-identically.
	var again bytes.Buffer
	RoutesText(&again, routes)
	if again.String() != out {
		t.Fatal("RoutesText is not deterministic")
	}
	// And an empty capture says so instead of printing a bare header.
	var empty bytes.Buffer
	RoutesText(&empty, nil)
	if !strings.Contains(empty.String(), "no servable routes") {
		t.Fatalf("got %q", empty.String())
	}
}

func TestRoutesTextAnnotatesReplayPositions(t *testing.T) {
	mk := func(ts string) har.Entry {
		e := entry("GET", "http://api.example.test/job", 200, "x")
		e.StartedDateTime = ts
		return e
	}
	_, routes, _ := buildAll(t, mk("2026-07-01T09:00:01Z"), mk("2026-07-01T09:00:02Z"))
	var buf bytes.Buffer
	RoutesText(&buf, routes)
	if !strings.Contains(buf.String(), "replay 1/2") || !strings.Contains(buf.String(), "replay 2/2") {
		t.Fatalf("replay annotations missing:\n%s", buf.String())
	}
}

func TestRoutesJSONShape(t *testing.T) {
	_, routes, _ := buildAll(t,
		entry("GET", "http://api.example.test/api/pets?limit=2", 200, `{"pets":[]}`),
	)
	var buf bytes.Buffer
	if err := RoutesJSON(&buf, routes); err != nil {
		t.Fatalf("RoutesJSON: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if got[0]["method"] != "GET" || got[0]["path"] != "/api/pets" || got[0]["query"] != "limit=2" {
		t.Fatalf("row = %v", got[0])
	}
	if got[0]["bodyBytes"] != float64(len(`{"pets":[]}`)) {
		t.Fatalf("bodyBytes = %v", got[0]["bodyBytes"])
	}
}

func TestShowTextIncludesRequestAndResponse(t *testing.T) {
	e := entry("POST", "http://api.example.test/api/pets", 201, `{"id":3}`)
	e.Request.PostData = &har.PostData{MimeType: "application/json", Text: `{"name":"Kuro"}`}
	e.Request.Headers = []har.NVP{{Name: "Accept", Value: "application/json"}}
	_, routes, _ := buildAll(t, e)
	var buf bytes.Buffer
	ShowText(&buf, routes[0])
	out := buf.String()
	for _, want := range []string{"POST /api/pets", "api.example.test", "Accept: application/json",
		`{"name":"Kuro"}`, "response 201", `{"id":3}`} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
}

func TestShowTextBodyPreviews(t *testing.T) {
	// Binary bodies are summarized, never dumped raw to the terminal.
	e := entry("GET", "http://api.example.test/logo.png", 200, "")
	e.Response.Content = har.Content{Size: 4, MimeType: "image/png", Text: "AAEC/w==", Encoding: "base64"}
	_, routes, _ := buildAll(t, e)
	var buf bytes.Buffer
	ShowText(&buf, routes[0])
	if !strings.Contains(buf.String(), "(binary,") {
		t.Fatalf("binary body should be summarized, got:\n%s", buf.String())
	}
	// Huge text bodies are truncated with a total note.
	big := entry("GET", "http://api.example.test/big", 200, strings.Repeat("a", 5000))
	_, routes, _ = buildAll(t, big)
	buf.Reset()
	ShowText(&buf, routes[0])
	if !strings.Contains(buf.String(), "total)") {
		t.Fatal("huge body should be truncated with a total note")
	}
	if buf.Len() > 4000 {
		t.Fatalf("output too large: %d bytes", buf.Len())
	}
}

func TestFormattingHelpers(t *testing.T) {
	sizes := map[int]string{
		0:       "0B",
		999:     "999B",
		2048:    "2.0kB",
		1 << 21: "2.0MB",
	}
	for n, want := range sizes {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
	mimes := map[string]string{
		"":                                "-",
		"application/json; charset=utf-8": "json",
		"application/vnd.api+json":        "json",
		"image/png":                       "png",
		"text/html":                       "html",
	}
	for in, want := range mimes {
		if got := shortMime(in); got != want {
			t.Errorf("shortMime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckCleanCapture(t *testing.T) {
	a, routes, skips := buildAll(t, entry("GET", "http://api.example.test/a", 200, "x"))
	rep := Check(a, routes, skips)
	if rep.Errors() || len(rep.Findings) != 0 {
		t.Fatalf("clean capture produced findings: %+v", rep.Findings)
	}
	var buf bytes.Buffer
	CheckText(&buf, "clean.har", rep)
	if !strings.Contains(buf.String(), "ready to serve") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestCheckErrorOnEmptyCapture(t *testing.T) {
	a := &har.Archive{Log: har.Log{Version: "1.2", Entries: []har.Entry{}}}
	rep := Check(a, nil, nil)
	if !rep.Errors() {
		t.Fatal("empty capture must be an error")
	}
}

func TestCheckReportsSkipsWithLevels(t *testing.T) {
	bad := entry("GET", "http://api.example.test/img", 200, "!!x!!")
	bad.Response.Content.Encoding = "base64"
	aborted := entry("GET", "http://api.example.test/gone", 0, "")
	a, routes, skips := buildAll(t, entry("GET", "http://api.example.test/ok", 200, "x"), bad, aborted)
	rep := Check(a, routes, skips)
	var errs, warns int
	for _, f := range rep.Findings {
		switch f.Level {
		case "error":
			errs++
		case "warning":
			warns++
		}
	}
	if errs != 1 || warns != 1 {
		t.Fatalf("errors=%d warnings=%d findings=%+v", errs, warns, rep.Findings)
	}
	if !rep.Errors() {
		t.Fatal("undecodable body must gate the exit code")
	}
}

func TestCheckWarnsOnMissingBodies(t *testing.T) {
	// content.size > 0 with empty text = exported without content.
	e := entry("GET", "http://api.example.test/data", 200, "")
	e.Response.Content.Size = 512
	a, routes, skips := buildAll(t, e)
	rep := Check(a, routes, skips)
	found := false
	for _, f := range rep.Findings {
		if f.Level == "warning" && strings.Contains(f.Message, "re-export with content") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing-body warning absent: %+v", rep.Findings)
	}
	// The warning must also fire when the recorded URL carries a query
	// string — the URL/path comparison has to ignore it.
	q := entry("GET", "http://api.example.test/data?limit=2", 200, "")
	q.Response.Content.Size = 512
	a, routes, skips = buildAll(t, q)
	rep = Check(a, routes, skips)
	found = false
	for _, f := range rep.Findings {
		if f.Level == "warning" && strings.Contains(f.Message, "re-export with content") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing-body warning absent for query-string URL: %+v", rep.Findings)
	}
	// A 204 is legitimately bodyless and must not trigger the warning.
	del := entry("DELETE", "http://api.example.test/a", 204, "")
	del.Response.Content.Size = 0
	a, routes, skips = buildAll(t, del)
	if rep := Check(a, routes, skips); len(rep.Findings) != 0 {
		t.Fatalf("204 must not warn about missing body: %+v", rep.Findings)
	}
}

func TestCheckInfoOnDuplicates(t *testing.T) {
	mk := func(ts string) har.Entry {
		e := entry("GET", "http://api.example.test/job", 200, "x")
		e.StartedDateTime = ts
		return e
	}
	a, routes, skips := buildAll(t, mk("2026-07-01T09:00:01Z"), mk("2026-07-01T09:00:02Z"))
	rep := Check(a, routes, skips)
	found := false
	for _, f := range rep.Findings {
		if f.Level == "info" && strings.Contains(f.Message, "recorded 2 times") {
			found = true
		}
	}
	if !found || rep.Errors() {
		t.Fatalf("duplicate info missing or wrongly fatal: %+v", rep.Findings)
	}
}

func TestCheckWarnsOnUnknownMajorVersion(t *testing.T) {
	a := &har.Archive{Log: har.Log{Version: "2.0", Entries: []har.Entry{
		entry("GET", "http://api.example.test/a", 200, "x"),
	}}}
	routes, skips, _ := match.BuildRoutes(a, match.BuildOptions{})
	rep := Check(a, routes, skips)
	found := false
	for _, f := range rep.Findings {
		if strings.Contains(f.Message, "untested") {
			found = true
		}
	}
	if !found {
		t.Fatalf("version warning missing: %+v", rep.Findings)
	}
}

func TestCheckJSONAlwaysHasFindingsArray(t *testing.T) {
	a, routes, skips := buildAll(t, entry("GET", "http://api.example.test/a", 200, "x"))
	var buf bytes.Buffer
	if err := CheckJSON(&buf, Check(a, routes, skips)); err != nil {
		t.Fatalf("CheckJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if _, ok := got["findings"].([]any); !ok {
		t.Fatalf("findings must be an array even when empty: %v", got)
	}
	if got["servable"] != float64(1) {
		t.Fatalf("servable = %v", got["servable"])
	}
}
