// Tests for the scoring matcher and replay strategies — the heart of
// harmock's determinism claim.
package match

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/JaydenCJ/harmock/internal/har"
)

// get builds an Incoming GET request for path with an optional raw query.
func get(path, rawQuery string) Incoming {
	q, _ := url.ParseQuery(rawQuery)
	return Incoming{Method: "GET", Path: path, Query: q, Header: http.Header{}}
}

// post builds an Incoming POST with a body.
func post(path, body string) Incoming {
	return Incoming{Method: "POST", Path: path, Query: url.Values{}, Header: http.Header{}, Body: []byte(body)}
}

// routesFor builds routes from entries with default options.
func routesFor(t *testing.T, entries ...har.Entry) []Route {
	t.Helper()
	routes, _ := mustBuild(t, archive(entries...), BuildOptions{})
	return routes
}

func TestMatchExactMethodAndPath(t *testing.T) {
	m := New(routesFor(t,
		entry("GET", "http://api.example.test/api/pets", 200, "pets"),
	), Options{})
	r, _ := m.Match(get("/api/pets", ""), StrategyFirst)
	if r == nil || string(r.RespBody) != "pets" {
		t.Fatalf("match = %+v", r)
	}
}

func TestMatchRequiresMethodAndPath(t *testing.T) {
	m := New(routesFor(t,
		entry("POST", "http://api.example.test/api/pets", 201, "created"),
		entry("GET", "http://api.example.test/api/pets", 200, "pets"),
	), Options{})
	if r, _ := m.Match(get("/api/pets/1", ""), StrategyFirst); r != nil {
		t.Fatalf("path mismatch must not match, got %+v", r)
	}
	in := Incoming{Method: "PUT", Path: "/api/pets", Query: url.Values{}, Header: http.Header{}}
	if r, _ := m.Match(in, StrategyFirst); r != nil {
		t.Fatalf("PUT must not match recorded GET/POST, got %+v", r)
	}
	// Method comparison is case-insensitive: curl -X get still works.
	lower := get("/api/pets", "")
	lower.Method = "get"
	if r, _ := m.Match(lower, StrategyFirst); r == nil || string(r.RespBody) != "pets" {
		t.Fatalf("lowercase method should match the GET recording, got %+v", r)
	}
}

func TestMatchPrefersExactQuery(t *testing.T) {
	// Two recordings of the same path with different queries: the exact
	// query must win regardless of recorded order.
	m := New(routesFor(t,
		entry("GET", "http://api.example.test/api/pets?limit=2", 200, "two"),
		entry("GET", "http://api.example.test/api/pets?limit=50", 200, "fifty"),
	), Options{})
	r, _ := m.Match(get("/api/pets", "limit=50"), StrategyFirst)
	if r == nil || string(r.RespBody) != "fifty" {
		t.Fatalf("want exact-query winner 'fifty', got %+v", r)
	}
}

func TestMatchPartialQueryOverlapStillMatches(t *testing.T) {
	// A live request with an extra param (say a feature flag) should still
	// hit the closest recording rather than 404.
	m := New(routesFor(t,
		entry("GET", "http://api.example.test/api/pets?limit=2", 200, "two"),
	), Options{})
	r, _ := m.Match(get("/api/pets", "limit=2&flag=on"), StrategyFirst)
	if r == nil || string(r.RespBody) != "two" {
		t.Fatalf("partial overlap should match, got %+v", r)
	}
}

func TestMatchQueryValueMismatchPicksBestCandidate(t *testing.T) {
	m := New(routesFor(t,
		entry("GET", "http://api.example.test/search?q=cats&page=1", 200, "cats1"),
		entry("GET", "http://api.example.test/search?q=dogs&page=1", 200, "dogs1"),
	), Options{})
	r, _ := m.Match(get("/search", "q=dogs&page=1"), StrategyFirst)
	if r == nil || string(r.RespBody) != "dogs1" {
		t.Fatalf("want q=dogs recording, got %+v", r)
	}
}

func TestMatchIgnoreQueryDropsCacheBusters(t *testing.T) {
	// `_=1699999999` style cache busters differ on every request; with
	// --ignore-query _ they must not break exact-query matching.
	m := New(routesFor(t,
		entry("GET", "http://api.example.test/feed?_=111&page=2", 200, "page2"),
	), Options{IgnoreQuery: []string{"_"}})
	r, _ := m.Match(get("/feed", "_=999&page=2"), StrategyFirst)
	if r == nil || string(r.RespBody) != "page2" {
		t.Fatalf("ignored key should not affect matching, got %+v", r)
	}
}

func TestMatchBodyExactBeatsDifferentBody(t *testing.T) {
	e1 := entry("POST", "http://api.example.test/api/pets", 201, "kuro-created")
	e1.Request.PostData = &har.PostData{MimeType: "application/json", Text: `{"name":"Kuro"}`}
	e2 := entry("POST", "http://api.example.test/api/pets", 201, "momo-created")
	e2.Request.PostData = &har.PostData{MimeType: "application/json", Text: `{"name":"Momo"}`}
	m := New(routesFor(t, e1, e2), Options{})
	r, _ := m.Match(post("/api/pets", `{"name":"Momo"}`), StrategyFirst)
	if r == nil || string(r.RespBody) != "momo-created" {
		t.Fatalf("body match should select the Momo recording, got %+v", r)
	}
}

func TestMatchBodyJSONKeyOrderInsensitive(t *testing.T) {
	// fetch() serializes keys in insertion order; a replayed curl command
	// often reorders them. Structural JSON equality must still score high.
	e1 := entry("POST", "http://api.example.test/api/pets", 201, "cat")
	e1.Request.PostData = &har.PostData{Text: `{"name":"Kuro","species":"cat"}`}
	e2 := entry("POST", "http://api.example.test/api/pets", 201, "dog")
	e2.Request.PostData = &har.PostData{Text: `{"name":"Hachi","species":"dog"}`}
	m := New(routesFor(t, e1, e2), Options{})
	r, _ := m.Match(post("/api/pets", `{"species":"dog","name":"Hachi"}`), StrategyFirst)
	if r == nil || string(r.RespBody) != "dog" {
		t.Fatalf("JSON-equal body should win, got %+v", r)
	}
}

func TestMatchBodyModes(t *testing.T) {
	// never: recorded order decides even when a body would disambiguate.
	e1 := entry("POST", "http://api.example.test/x", 200, "first")
	e1.Request.PostData = &har.PostData{Text: `{"a":1}`}
	e2 := entry("POST", "http://api.example.test/x", 200, "second")
	e2.Request.PostData = &har.PostData{Text: `{"a":2}`}
	m := New(routesFor(t, e1, e2), Options{MatchBody: BodyNever})
	r, _ := m.Match(post("/x", `{"a":2}`), StrategyFirst)
	if r == nil || string(r.RespBody) != "first" {
		t.Fatalf("BodyNever should ignore bodies, got %+v", r)
	}

	// always: an incoming request without a body must prefer the bodyless
	// recording over one that expects a payload.
	e3 := entry("POST", "http://api.example.test/y", 200, "with-body")
	e3.Request.PostData = &har.PostData{Text: `{"a":1}`}
	e4 := entry("POST", "http://api.example.test/y", 200, "no-body")
	m = New(routesFor(t, e3, e4), Options{MatchBody: BodyAlways})
	r, _ = m.Match(post("/y", ""), StrategyFirst)
	if r == nil || string(r.RespBody) != "no-body" {
		t.Fatalf("BodyAlways should prefer the bodyless recording, got %+v", r)
	}
}

func TestHeaderMatchingIsOptIn(t *testing.T) {
	e1 := entry("GET", "http://api.example.test/me", 200, "alice")
	e1.Request.Headers = []har.NVP{{Name: "Authorization", Value: "Bearer alice"}}
	e2 := entry("GET", "http://api.example.test/me", 200, "bob")
	e2.Request.Headers = []har.NVP{{Name: "Authorization", Value: "Bearer bob"}}

	// Without opt-in, headers are invisible: a bare request still matches.
	m := New(routesFor(t, e1, e2), Options{})
	if r, _ := m.Match(get("/me", ""), StrategyFirst); r == nil {
		t.Fatal("headers must not affect matching unless opted in")
	}

	// With --match-header Authorization, the right identity wins.
	m = New(routesFor(t, e1, e2), Options{MatchHeaders: []string{"Authorization"}})
	in := get("/me", "")
	in.Header.Set("Authorization", "Bearer bob")
	r, _ := m.Match(in, StrategyFirst)
	if r == nil || string(r.RespBody) != "bob" {
		t.Fatalf("header matching should select bob, got %+v", r)
	}
}

// jobEntries returns three recordings of the same request in time order.
func jobEntries() []har.Entry {
	mk := func(ts, body string) har.Entry {
		e := entry("GET", "http://api.example.test/api/jobs/42", 200, body)
		e.StartedDateTime = ts
		return e
	}
	return []har.Entry{
		mk("2026-07-01T09:00:01Z", "pending"),
		mk("2026-07-01T09:00:02Z", "running"),
		mk("2026-07-01T09:00:03Z", "done"),
	}
}

func TestSequentialWalksDuplicatesInRecordedOrder(t *testing.T) {
	m := New(routesFor(t, jobEntries()...), Options{})
	want := []string{"pending", "running", "done"}
	for i, w := range want {
		r, _ := m.Match(get("/api/jobs/42", ""), StrategySequential)
		if r == nil || string(r.RespBody) != w {
			t.Fatalf("call %d: got %+v, want %q", i, r, w)
		}
	}
}

func TestSequentialSticksAtLastRecording(t *testing.T) {
	// Once the recordings run out the mock must keep answering (with the
	// final state), not start failing mid-test-suite.
	m := New(routesFor(t, jobEntries()...), Options{})
	for i := 0; i < 5; i++ {
		m.Match(get("/api/jobs/42", ""), StrategySequential)
	}
	r, _ := m.Match(get("/api/jobs/42", ""), StrategySequential)
	if r == nil || string(r.RespBody) != "done" {
		t.Fatalf("exhausted sequence should stick at last, got %+v", r)
	}
}

func TestSequentialCursorsIndependentPerRequestKey(t *testing.T) {
	entries := jobEntries()
	other := entry("GET", "http://api.example.test/api/pets", 200, "pets")
	entries = append(entries, other)
	m := New(routesFor(t, entries...), Options{})
	m.Match(get("/api/jobs/42", ""), StrategySequential) // consume "pending"
	if r, _ := m.Match(get("/api/pets", ""), StrategySequential); r == nil {
		t.Fatal("other key should be unaffected")
	}
	r, _ := m.Match(get("/api/jobs/42", ""), StrategySequential)
	if string(r.RespBody) != "running" {
		t.Fatalf("cursor should be per-key, got %q", r.RespBody)
	}
}

func TestResetRewindsSequentialCursors(t *testing.T) {
	m := New(routesFor(t, jobEntries()...), Options{})
	m.Match(get("/api/jobs/42", ""), StrategySequential)
	m.Match(get("/api/jobs/42", ""), StrategySequential)
	m.Reset()
	r, _ := m.Match(get("/api/jobs/42", ""), StrategySequential)
	if string(r.RespBody) != "pending" {
		t.Fatalf("after Reset want 'pending', got %q", r.RespBody)
	}
}

func TestStrategyFirstAndLastArePinned(t *testing.T) {
	m := New(routesFor(t, jobEntries()...), Options{})
	for i := 0; i < 3; i++ {
		r, _ := m.Match(get("/api/jobs/42", ""), StrategyFirst)
		if string(r.RespBody) != "pending" {
			t.Fatalf("first, call %d: got %q", i, r.RespBody)
		}
		r, _ = m.Match(get("/api/jobs/42", ""), StrategyLast)
		if string(r.RespBody) != "done" {
			t.Fatalf("last, call %d: got %q", i, r.RespBody)
		}
	}
}

func TestMatchIsDeterministicAcrossResets(t *testing.T) {
	// The determinism promise: same capture + same request sequence =
	// same responses, every run.
	m := New(routesFor(t, jobEntries()...), Options{})
	run := func() []string {
		var out []string
		for i := 0; i < 4; i++ {
			r, _ := m.Match(get("/api/jobs/42", ""), StrategySequential)
			out = append(out, string(r.RespBody))
		}
		return out
	}
	first := run()
	m.Reset()
	second := run()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("run diverged at %d: %v vs %v", i, first, second)
		}
	}
}

func TestSuggestSamePathDifferentMethod(t *testing.T) {
	m := New(routesFor(t,
		entry("POST", "http://api.example.test/api/pets", 201, "x"),
	), Options{})
	_, sugg := m.Match(get("/api/pets", ""), StrategyFirst)
	if len(sugg) != 1 || sugg[0].Method != "POST" || sugg[0].Path != "/api/pets" {
		t.Fatalf("suggestions = %+v", sugg)
	}
}

func TestSuggestSimilarPathsRankedByPrefix(t *testing.T) {
	m := New(routesFor(t,
		entry("GET", "http://api.example.test/api/pets/1", 200, "x"),
		entry("GET", "http://api.example.test/api/toys", 200, "y"),
	), Options{})
	_, sugg := m.Match(get("/api/pets/2", ""), StrategyFirst)
	if len(sugg) == 0 || sugg[0].Path != "/api/pets/1" {
		t.Fatalf("closest path should rank first: %+v", sugg)
	}
}

func TestSuggestCapsAtThree(t *testing.T) {
	m := New(routesFor(t,
		entry("GET", "http://api.example.test/api/a", 200, "a"),
		entry("GET", "http://api.example.test/api/b", 200, "b"),
		entry("GET", "http://api.example.test/api/c", 200, "c"),
		entry("GET", "http://api.example.test/api/d", 200, "d"),
		entry("POST", "http://api.example.test/api/x", 200, "x"),
	), Options{})
	_, sugg := m.Match(get("/api/x", ""), StrategyFirst)
	if len(sugg) > 3 {
		t.Fatalf("suggestions must cap at 3, got %d", len(sugg))
	}
	if sugg[0].Method != "POST" {
		t.Fatalf("same-path-other-method must rank first: %+v", sugg)
	}
}

func TestParseEnumFlags(t *testing.T) {
	for _, ok := range []string{"sequential", "first", "last"} {
		if _, valid := ParseStrategy(ok); !valid {
			t.Errorf("ParseStrategy(%q) should be valid", ok)
		}
	}
	if _, valid := ParseStrategy("random"); valid {
		t.Error("ParseStrategy(random) should be invalid")
	}
	for _, ok := range []string{"auto", "always", "never"} {
		if _, valid := ParseBodyMode(ok); !valid {
			t.Errorf("ParseBodyMode(%q) should be valid", ok)
		}
	}
	if _, valid := ParseBodyMode("strict"); valid {
		t.Error("ParseBodyMode(strict) should be invalid")
	}
}
