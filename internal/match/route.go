// Route construction: turning raw HAR entries into servable routes,
// with per-entry skip reasons for everything that cannot be served.
package match

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/JaydenCJ/harmock/internal/har"
)

// Route is one servable HAR entry, pre-parsed for fast matching.
type Route struct {
	Index  int // position in recorded (time-sorted) order, 0-based
	Method string
	Host   string
	Path   string
	Query  url.Values
	Header http.Header // recorded request headers
	Body   []byte      // recorded request body (nil when none)

	Status       int
	RespHeader   http.Header
	RespBody     []byte
	RespMimeType string
	TimeMS       float64 // recorded round-trip time, for --latency record
}

// Skip explains why an entry was excluded from serving.
type Skip struct {
	Index  int
	Reason string
}

// BuildOptions filter and rewrite entries while building routes.
type BuildOptions struct {
	Hosts       []string // serve only entries whose URL host matches (empty = all)
	StripPrefix string   // remove this leading path segment from recorded paths
}

// hostAllowed reports whether host (which may include a port) passes the
// --host filter. Comparison ignores the port and is case-insensitive.
func hostAllowed(host string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	bare := strings.ToLower(host)
	if i := strings.LastIndex(bare, ":"); i >= 0 && !strings.HasSuffix(bare, "]") {
		bare = bare[:i]
	}
	for _, a := range allowed {
		want := strings.ToLower(a)
		if i := strings.LastIndex(want, ":"); i >= 0 && !strings.HasSuffix(want, "]") {
			want = want[:i]
		}
		if bare == want {
			return true
		}
	}
	return false
}

// headerFromNVP converts HAR name/value pairs to an http.Header, dropping
// HTTP/2 pseudo-headers (":method", ":path", …) that browsers record.
func headerFromNVP(pairs []har.NVP) http.Header {
	h := make(http.Header, len(pairs))
	for _, p := range pairs {
		if strings.HasPrefix(p.Name, ":") {
			continue
		}
		h.Add(p.Name, p.Value)
	}
	return h
}

// BuildRoutes converts a parsed archive into servable routes. Entries are
// first stably sorted by startedDateTime so Route.Index reflects recorded
// order, which is what sequential replay walks through.
func BuildRoutes(a *har.Archive, opt BuildOptions) ([]Route, []Skip, error) {
	entries := make([]har.Entry, len(a.Log.Entries))
	copy(entries, a.Log.Entries)
	har.SortEntries(entries)

	var routes []Route
	var skips []Skip
	for i, e := range entries {
		u, err := url.Parse(e.Request.URL)
		if err != nil || e.Request.URL == "" {
			skips = append(skips, Skip{i, "unparsable request URL"})
			continue
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			skips = append(skips, Skip{i, fmt.Sprintf("non-HTTP scheme %q", u.Scheme)})
			continue
		}
		if e.Response.Status == 0 {
			skips = append(skips, Skip{i, "aborted request (response status 0)"})
			continue
		}
		if !hostAllowed(u.Host, opt.Hosts) {
			skips = append(skips, Skip{i, fmt.Sprintf("host %q filtered out", u.Host)})
			continue
		}
		body, err := e.Response.Content.Body()
		if err != nil {
			skips = append(skips, Skip{i, fmt.Sprintf("undecodable response body: %v", err)})
			continue
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		if opt.StripPrefix != "" && strings.HasPrefix(path, opt.StripPrefix) {
			path = strings.TrimPrefix(path, opt.StripPrefix)
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
		}
		routes = append(routes, Route{
			Index:        len(routes),
			Method:       strings.ToUpper(e.Request.Method),
			Host:         u.Host,
			Path:         path,
			Query:        u.Query(),
			Header:       headerFromNVP(e.Request.Headers),
			Body:         e.Request.Body(),
			Status:       e.Response.Status,
			RespHeader:   headerFromNVP(e.Response.Headers),
			RespBody:     body,
			RespMimeType: e.Response.Content.MimeType,
			TimeMS:       e.Time,
		})
	}
	return routes, skips, nil
}
