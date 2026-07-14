// Tests for route construction: URL parsing, filtering, prefix stripping,
// and the skip-with-reason contract for unservable entries.
package match

import (
	"strings"
	"testing"

	"github.com/JaydenCJ/harmock/internal/har"
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

// archive wraps entries into a parsed archive.
func archive(entries ...har.Entry) *har.Archive {
	return &har.Archive{Log: har.Log{Version: "1.2", Entries: entries}}
}

func mustBuild(t *testing.T, a *har.Archive, opt BuildOptions) ([]Route, []Skip) {
	t.Helper()
	routes, skips, err := BuildRoutes(a, opt)
	if err != nil {
		t.Fatalf("BuildRoutes: %v", err)
	}
	return routes, skips
}

func TestBuildRoutesBasicFields(t *testing.T) {
	routes, skips := mustBuild(t, archive(
		entry("get", "http://api.example.test/api/pets?limit=2", 200, `{"ok":true}`),
	), BuildOptions{})
	if len(skips) != 0 || len(routes) != 1 {
		t.Fatalf("routes=%d skips=%v", len(routes), skips)
	}
	r := routes[0]
	if r.Method != "GET" { // method is uppercased
		t.Errorf("Method = %q", r.Method)
	}
	if r.Host != "api.example.test" || r.Path != "/api/pets" {
		t.Errorf("Host/Path = %q %q", r.Host, r.Path)
	}
	if r.Query.Get("limit") != "2" {
		t.Errorf("Query = %v", r.Query)
	}
	if r.Status != 200 || string(r.RespBody) != `{"ok":true}` {
		t.Errorf("response = %d %q", r.Status, r.RespBody)
	}
	if r.TimeMS != 10 {
		t.Errorf("TimeMS = %v (recorded latency must survive for --latency record)", r.TimeMS)
	}
}

func TestBuildRoutesSkipsAbortedRequests(t *testing.T) {
	// DevTools records cancelled/failed requests with status 0; serving
	// them would replay a response that never existed.
	routes, skips := mustBuild(t, archive(
		entry("GET", "http://api.example.test/ok", 200, "ok"),
		entry("GET", "http://api.example.test/aborted", 0, ""),
	), BuildOptions{})
	if len(routes) != 1 || len(skips) != 1 {
		t.Fatalf("routes=%d skips=%d", len(routes), len(skips))
	}
	if !strings.Contains(skips[0].Reason, "aborted") {
		t.Errorf("skip reason = %q", skips[0].Reason)
	}
}

func TestBuildRoutesSkipsNonHTTPSchemes(t *testing.T) {
	routes, skips := mustBuild(t, archive(
		entry("GET", "ws://api.example.test/socket", 101, ""),
		entry("GET", "data:text/plain,hi", 200, "hi"),
	), BuildOptions{})
	if len(routes) != 0 || len(skips) != 2 {
		t.Fatalf("routes=%d skips=%v", len(routes), skips)
	}
}

func TestBuildRoutesSkipsUndecodableBody(t *testing.T) {
	e := entry("GET", "http://api.example.test/img", 200, "!!bad-base64!!")
	e.Response.Content.Encoding = "base64"
	routes, skips := mustBuild(t, archive(e), BuildOptions{})
	if len(routes) != 0 || len(skips) != 1 {
		t.Fatalf("routes=%d skips=%v", len(routes), skips)
	}
	if !strings.Contains(skips[0].Reason, "undecodable") {
		t.Errorf("skip reason = %q", skips[0].Reason)
	}
}

func TestBuildRoutesHostFilter(t *testing.T) {
	routes, skips := mustBuild(t, archive(
		entry("GET", "http://api.example.test/a", 200, "a"),
		entry("GET", "http://cdn.example.test/b", 200, "b"),
	), BuildOptions{Hosts: []string{"api.example.test"}})
	if len(routes) != 1 || routes[0].Path != "/a" {
		t.Fatalf("routes = %+v", routes)
	}
	if len(skips) != 1 || !strings.Contains(skips[0].Reason, "filtered") {
		t.Fatalf("skips = %+v", skips)
	}
	// The filter compares bare hostnames: port and case must not matter.
	routes, _ = mustBuild(t, archive(
		entry("GET", "http://API.Example.Test:8443/a", 200, "a"),
	), BuildOptions{Hosts: []string{"api.example.test"}})
	if len(routes) != 1 {
		t.Fatalf("host filter should match ignoring port and case, routes=%d", len(routes))
	}
}

func TestBuildRoutesStripPrefix(t *testing.T) {
	routes, _ := mustBuild(t, archive(
		entry("GET", "http://api.example.test/v2/pets", 200, "x"),
	), BuildOptions{StripPrefix: "/v2"})
	if routes[0].Path != "/pets" {
		t.Fatalf("Path = %q, want /pets", routes[0].Path)
	}
	// Stripping the whole path must still leave a servable "/".
	routes, _ = mustBuild(t, archive(
		entry("GET", "http://api.example.test/v2", 200, "x"),
	), BuildOptions{StripPrefix: "/v2"})
	if routes[0].Path != "/" {
		t.Fatalf("Path = %q, want /", routes[0].Path)
	}
}

func TestBuildRoutesEmptyPathBecomesSlash(t *testing.T) {
	routes, _ := mustBuild(t, archive(
		entry("GET", "http://api.example.test", 200, "root"),
	), BuildOptions{})
	if routes[0].Path != "/" {
		t.Fatalf("Path = %q, want /", routes[0].Path)
	}
}

func TestBuildRoutesDropsHTTP2PseudoHeaders(t *testing.T) {
	e := entry("GET", "https://api.example.test/a", 200, "a")
	e.Request.Headers = []har.NVP{
		{Name: ":method", Value: "GET"},
		{Name: ":path", Value: "/a"},
		{Name: "Accept", Value: "application/json"},
	}
	routes, _ := mustBuild(t, archive(e), BuildOptions{})
	if got := routes[0].Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	if len(routes[0].Header) != 1 {
		t.Fatalf("pseudo-headers leaked: %v", routes[0].Header)
	}
}

func TestBuildRoutesIndexFollowsRecordedTime(t *testing.T) {
	// Entries arrive out of order in the file; Route.Index must reflect
	// startedDateTime order because sequential replay walks it.
	e1 := entry("GET", "http://api.example.test/x", 200, "second")
	e1.StartedDateTime = "2026-07-01T09:00:05Z"
	e2 := entry("GET", "http://api.example.test/x", 200, "first")
	e2.StartedDateTime = "2026-07-01T09:00:01Z"
	routes, _ := mustBuild(t, archive(e1, e2), BuildOptions{})
	if string(routes[0].RespBody) != "first" || string(routes[1].RespBody) != "second" {
		t.Fatalf("recorded order not honored: %q, %q", routes[0].RespBody, routes[1].RespBody)
	}
	if routes[0].Index != 0 || routes[1].Index != 1 {
		t.Fatalf("indices = %d, %d", routes[0].Index, routes[1].Index)
	}
}
