// Tests for the HTTP handler: replay fidelity, hop-by-hop hygiene, the
// unmatched payload, CORS override, latency simulation, and the admin
// surface. All requests go through httptest recorders — no sockets.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// handlerFor builds a Handler over entries with cfg.
func handlerFor(t *testing.T, cfg Config, entries ...har.Entry) *Handler {
	t.Helper()
	routes, _, err := match.BuildRoutes(
		&har.Archive{Log: har.Log{Version: "1.2", Entries: entries}}, match.BuildOptions{})
	if err != nil {
		t.Fatalf("BuildRoutes: %v", err)
	}
	return New(match.New(routes, match.Options{}), cfg)
}

// do runs one request through h and returns the recorder.
func do(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestServeReplaysStatusAndBody(t *testing.T) {
	h := handlerFor(t, Config{}, entry("GET", "http://api.example.test/api/pets", 200, `{"pets":[]}`))
	w := do(h, "GET", "/api/pets", "")
	if w.Code != 200 || w.Body.String() != `{"pets":[]}` {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("X-Harmock-Entry"); got != "0" {
		t.Fatalf("X-Harmock-Entry = %q (every reply must name its entry)", got)
	}
}

func TestServeReplaysRecordedHeaders(t *testing.T) {
	e := entry("GET", "http://api.example.test/a", 200, "x")
	e.Response.Headers = []har.NVP{
		{Name: "X-Request-Id", Value: "a1b2"},
		{Name: "Cache-Control", Value: "max-age=60"},
	}
	w := do(handlerFor(t, Config{}, e), "GET", "/a", "")
	if w.Header().Get("X-Request-Id") != "a1b2" || w.Header().Get("Cache-Control") != "max-age=60" {
		t.Fatalf("recorded headers missing: %v", w.Header())
	}
}

func TestServeStripsHopByHopAndEncodingHeaders(t *testing.T) {
	// The capture says gzip, but HAR stores the *decoded* body; replaying
	// Content-Encoding would make every client fail to decompress.
	e := entry("GET", "http://api.example.test/a", 200, `{"ok":true}`)
	e.Response.Headers = []har.NVP{
		{Name: "Content-Encoding", Value: "gzip"},
		{Name: "Transfer-Encoding", Value: "chunked"},
		{Name: "Connection", Value: "keep-alive"},
		{Name: "Content-Length", Value: "9999"},
	}
	w := do(handlerFor(t, Config{}, e), "GET", "/a", "")
	for _, name := range []string{"Content-Encoding", "Transfer-Encoding", "Connection"} {
		if got := w.Header().Get(name); got != "" {
			t.Errorf("%s should be stripped, got %q", name, got)
		}
	}
	if got := w.Header().Get("Content-Length"); got != "11" {
		t.Errorf("Content-Length should be recomputed to 11, got %q", got)
	}
}

func TestServeSetsContentTypeFromMimeWhenMissing(t *testing.T) {
	// Entry has a MIME type in content but no Content-Type header row.
	w := do(handlerFor(t, Config{}, entry("GET", "http://api.example.test/a", 200, "{}")), "GET", "/a", "")
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestServeHeadOmitsBody(t *testing.T) {
	e := entry("HEAD", "http://api.example.test/a", 200, "should-not-be-sent")
	w := do(handlerFor(t, Config{}, e), "HEAD", "/a", "")
	if w.Body.Len() != 0 {
		t.Fatalf("HEAD response carried a body: %q", w.Body.String())
	}
	if w.Header().Get("Content-Length") != "18" {
		t.Fatalf("HEAD should keep the entity Content-Length, got %q", w.Header().Get("Content-Length"))
	}
}

func TestServeBinaryBodyByteIdentical(t *testing.T) {
	e := entry("GET", "http://api.example.test/logo.png", 200, "")
	e.Response.Content = har.Content{
		Size: 5, MimeType: "image/png",
		Text: "AAECA/8=", Encoding: "base64", // bytes 0,1,2,3,255
	}
	w := do(handlerFor(t, Config{}, e), "GET", "/logo.png", "")
	got := w.Body.Bytes()
	want := []byte{0, 1, 2, 3, 255}
	if len(got) != len(want) {
		t.Fatalf("body length %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestUnmatchedReturnsDiagnosableJSON(t *testing.T) {
	h := handlerFor(t, Config{}, entry("GET", "http://api.example.test/a", 200, "x"))
	w := do(h, "GET", "/nope", "")
	if w.Code != 404 {
		t.Fatalf("code = %d", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if payload["method"] != "GET" || payload["path"] != "/nope" {
		t.Fatalf("payload = %v", payload)
	}
	// --fallback-status changes the code without changing the payload.
	h = handlerFor(t, Config{FallbackStatus: 501}, entry("GET", "http://api.example.test/a", 200, "x"))
	if w := do(h, "GET", "/nope", ""); w.Code != 501 {
		t.Fatalf("code = %d, want 501", w.Code)
	}
}

func TestUnmatchedIncludesSuggestions(t *testing.T) {
	h := handlerFor(t, Config{}, entry("POST", "http://api.example.test/api/pets", 201, "x"))
	w := do(h, "GET", "/api/pets", "")
	if !strings.Contains(w.Body.String(), "same path, different method") {
		t.Fatalf("suggestions missing: %s", w.Body.String())
	}
}

func TestSequentialReplayOverHTTP(t *testing.T) {
	mk := func(ts, body string) har.Entry {
		e := entry("GET", "http://api.example.test/job", 200, body)
		e.StartedDateTime = ts
		return e
	}
	h := handlerFor(t, Config{},
		mk("2026-07-01T09:00:01Z", "pending"),
		mk("2026-07-01T09:00:02Z", "done"))
	for _, want := range []string{"pending", "done", "done"} {
		if got := do(h, "GET", "/job", "").Body.String(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestAdminHealth(t *testing.T) {
	h := handlerFor(t, Config{}, entry("GET", "http://api.example.test/a", 200, "x"))
	w := do(h, "GET", "/__harmock__/health", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"routes": 1`) {
		t.Fatalf("health = %d %s", w.Code, w.Body.String())
	}
}

func TestAdminRoutesListsEverything(t *testing.T) {
	h := handlerFor(t, Config{},
		entry("GET", "http://api.example.test/a", 200, "aa"),
		entry("POST", "http://api.example.test/b", 201, "bbb"))
	w := do(h, "GET", "/__harmock__/routes", "")
	var infos []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &infos); err != nil {
		t.Fatalf("routes payload: %v", err)
	}
	if len(infos) != 2 || infos[1]["method"] != "POST" || infos[1]["bodyBytes"] != float64(3) {
		t.Fatalf("infos = %v", infos)
	}
}

func TestAdminResetRewindsReplay(t *testing.T) {
	mk := func(ts, body string) har.Entry {
		e := entry("GET", "http://api.example.test/job", 200, body)
		e.StartedDateTime = ts
		return e
	}
	h := handlerFor(t, Config{},
		mk("2026-07-01T09:00:01Z", "pending"),
		mk("2026-07-01T09:00:02Z", "done"))
	do(h, "GET", "/job", "")
	do(h, "GET", "/job", "")
	if w := do(h, "POST", "/__harmock__/reset", ""); w.Code != 200 {
		t.Fatalf("reset = %d", w.Code)
	}
	if got := do(h, "GET", "/job", "").Body.String(); got != "pending" {
		t.Fatalf("after reset got %q, want pending", got)
	}
}

func TestAdminRejectsUnknownRequests(t *testing.T) {
	h := handlerFor(t, Config{}, entry("GET", "http://api.example.test/a", 200, "x"))
	// reset is POST-only: a GET must not silently rewind cursors.
	if w := do(h, "GET", "/__harmock__/reset", ""); w.Code != 404 {
		t.Fatalf("GET reset = %d, want 404", w.Code)
	}
	w := do(h, "GET", "/__harmock__/whatever", "")
	if w.Code != 404 || !strings.Contains(w.Body.String(), "unknown admin endpoint") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestNoAdminDisablesAdminSurface(t *testing.T) {
	// With --no-admin, /__harmock__/ paths fall through to matching and
	// produce the standard unmatched payload — nothing is reserved.
	h := handlerFor(t, Config{NoAdmin: true}, entry("GET", "http://api.example.test/a", 200, "x"))
	w := do(h, "GET", "/__harmock__/health", "")
	if !strings.Contains(w.Body.String(), "no recorded entry") {
		t.Fatalf("admin should be off: %s", w.Body.String())
	}
}

func TestCORSOverrideOnMatchedResponse(t *testing.T) {
	e := entry("GET", "http://api.example.test/a", 200, "x")
	e.Response.Headers = []har.NVP{{Name: "Access-Control-Allow-Origin", Value: "https://prod.example.test"}}

	// Without --cors nothing is added and the recorded ACAO is replayed.
	plain := do(handlerFor(t, Config{}, e), "GET", "/a", "")
	if got := plain.Header().Get("Access-Control-Allow-Origin"); got != "https://prod.example.test" {
		t.Fatalf("recorded ACAO should replay verbatim by default, got %q", got)
	}

	h := handlerFor(t, Config{CORS: true}, e)
	req := httptest.NewRequest("GET", "/a", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:5173" {
		t.Fatalf("ACAO = %q (recorded value must be overridden)", got)
	}
	if w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("credentials header missing")
	}
}

func TestCORSAnswersUnrecordedPreflight(t *testing.T) {
	// Browsers send OPTIONS preflights that were never captured (the
	// recording ran same-origin). --cors must answer them itself.
	h := handlerFor(t, Config{CORS: true}, entry("GET", "http://api.example.test/a", 200, "x"))
	w := do(h, "OPTIONS", "/a", "")
	if w.Code != 204 {
		t.Fatalf("preflight = %d, want 204", w.Code)
	}
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "GET") {
		t.Fatalf("allow-methods missing: %v", w.Header())
	}
}

// sleepRecorder captures requested sleep durations instead of sleeping.
type sleepRecorder struct{ slept []time.Duration }

func (s *sleepRecorder) sleep(d time.Duration) { s.slept = append(s.slept, d) }

func TestLatencyNoneAndFixed(t *testing.T) {
	// Default: never sleep, even when the entry recorded 500 ms.
	rec := &sleepRecorder{}
	e := entry("GET", "http://api.example.test/a", 200, "x")
	e.Time = 500
	do(handlerFor(t, Config{Sleep: rec.sleep}, e), "GET", "/a", "")
	if len(rec.slept) != 0 {
		t.Fatalf("default latency must not sleep, slept %v", rec.slept)
	}
	// --latency 150 sleeps exactly the fixed amount.
	rec = &sleepRecorder{}
	h := handlerFor(t, Config{Latency: LatencyFixed, LatencyMS: 150, Sleep: rec.sleep},
		entry("GET", "http://api.example.test/a", 200, "x"))
	do(h, "GET", "/a", "")
	if len(rec.slept) != 1 || rec.slept[0] != 150*time.Millisecond {
		t.Fatalf("slept %v, want [150ms]", rec.slept)
	}
}

func TestLatencyRecordUsesRecordedTimeWithCap(t *testing.T) {
	rec := &sleepRecorder{}
	e := entry("GET", "http://api.example.test/a", 200, "x")
	e.Time = 250
	h := handlerFor(t, Config{Latency: LatencyRecord, Sleep: rec.sleep}, e)
	do(h, "GET", "/a", "")
	if len(rec.slept) != 1 || rec.slept[0] != 250*time.Millisecond {
		t.Fatalf("slept %v, want [250ms]", rec.slept)
	}
	// A 45-second recorded timeout must not freeze the mock for 45s.
	rec = &sleepRecorder{}
	e.Time = 45000
	h = handlerFor(t, Config{Latency: LatencyRecord, LatencyCapMS: 2000, Sleep: rec.sleep}, e)
	do(h, "GET", "/a", "")
	if len(rec.slept) != 1 || rec.slept[0] != 2*time.Second {
		t.Fatalf("slept %v, want [2s]", rec.slept)
	}
}

func TestLoggerReceivesRequestLines(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) {
		lines = append(lines, format)
	}
	h := handlerFor(t, Config{Logf: logf}, entry("GET", "http://api.example.test/a", 200, "x"))
	do(h, "GET", "/a", "")
	do(h, "GET", "/miss", "")
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d", len(lines))
	}
}
