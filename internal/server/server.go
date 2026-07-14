// Package server turns matched HAR routes into a live http.Handler: it
// replays recorded status lines, headers, and bodies, answers unmatched
// requests with a diagnosable JSON payload, and exposes a tiny admin
// surface under /__harmock__/.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JaydenCJ/harmock/internal/match"
)

// AdminPrefix is the path prefix reserved for harmock's own endpoints.
const AdminPrefix = "/__harmock__/"

// maxRequestBody bounds how much of an incoming body is read for matching.
const maxRequestBody = 10 << 20 // 10 MiB

// LatencyMode controls response-delay simulation.
type LatencyMode string

const (
	// LatencyNone replies immediately. The default: tests want speed.
	LatencyNone LatencyMode = "none"
	// LatencyRecord sleeps for each entry's recorded round-trip time
	// (capped by Config.LatencyCap).
	LatencyRecord LatencyMode = "record"
	// LatencyFixed sleeps a fixed number of milliseconds per response.
	LatencyFixed LatencyMode = "fixed"
)

// Config tunes the handler.
type Config struct {
	Strategy       match.Strategy
	FallbackStatus int         // status for unmatched requests (default 404)
	CORS           bool        // overwrite recorded CORS with permissive headers
	Latency        LatencyMode // default LatencyNone
	LatencyMS      int         // used by LatencyFixed
	LatencyCapMS   int         // ceiling for LatencyRecord (default 3000)
	NoAdmin        bool        // disable the /__harmock__/ endpoints
	Logf           func(format string, args ...any)
	Sleep          func(d time.Duration) // injectable for tests
}

// Handler serves a matched HAR capture.
type Handler struct {
	m   *match.Matcher
	cfg Config
}

// RouteCount reports how many routes the handler serves.
func (h *Handler) RouteCount() int { return len(h.m.Routes()) }

// New builds a Handler over matcher with cfg, applying defaults.
func New(m *match.Matcher, cfg Config) *Handler {
	if cfg.Strategy == "" {
		cfg.Strategy = match.StrategySequential
	}
	if cfg.FallbackStatus == 0 {
		cfg.FallbackStatus = http.StatusNotFound
	}
	if cfg.Latency == "" {
		cfg.Latency = LatencyNone
	}
	if cfg.LatencyCapMS == 0 {
		cfg.LatencyCapMS = 3000
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	return &Handler{m: m, cfg: cfg}
}

// hopByHop lists headers that must not be replayed verbatim: connection
// management belongs to this server, and bodies are served decoded, so the
// recorded Content-Encoding/Content-Length would corrupt the response.
var hopByHop = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Proxy-Connection":    true,
	"Te":                  true,
	"Trailer":             true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Content-Encoding":    true,
	"Content-Length":      true,
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.NoAdmin && strings.HasPrefix(r.URL.Path, AdminPrefix) {
		h.serveAdmin(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		http.Error(w, "harmock: failed to read request body", http.StatusBadRequest)
		return
	}

	in := match.Incoming{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: r.Header,
		Body:   body,
	}
	route, suggestions := h.m.Match(in, h.cfg.Strategy)
	if route == nil {
		if h.cfg.CORS && r.Method == http.MethodOptions {
			// Preflight for a recorded non-OPTIONS route: answer it ourselves.
			h.writeCORS(w, r)
			w.WriteHeader(http.StatusNoContent)
			h.cfg.Logf("<- %s %s 204 (cors preflight)", r.Method, r.URL.RequestURI())
			return
		}
		h.writeUnmatched(w, r, suggestions)
		return
	}

	h.sleepFor(route)

	hdr := w.Header()
	for name, vals := range route.RespHeader {
		if hopByHop[http.CanonicalHeaderKey(name)] {
			continue
		}
		for _, v := range vals {
			hdr.Add(name, v)
		}
	}
	if hdr.Get("Content-Type") == "" && route.RespMimeType != "" {
		hdr.Set("Content-Type", route.RespMimeType)
	}
	if h.cfg.CORS {
		h.writeCORS(w, r)
	}
	hdr.Set("Content-Length", strconv.Itoa(len(route.RespBody)))
	hdr.Set("X-Harmock-Entry", strconv.Itoa(route.Index))
	w.WriteHeader(route.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(route.RespBody)
	}
	h.cfg.Logf("<- %s %s %d (entry #%d)", r.Method, r.URL.RequestURI(), route.Status, route.Index)
}

// writeCORS overwrites any recorded CORS decision with a permissive one so
// a frontend on another local port can talk to the mock without ceremony.
func (h *Handler) writeCORS(w http.ResponseWriter, r *http.Request) {
	hdr := w.Header()
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	hdr.Set("Access-Control-Allow-Origin", origin)
	hdr.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
	hdr.Set("Access-Control-Allow-Headers", "*")
	hdr.Set("Access-Control-Allow-Credentials", "true")
	hdr.Del("Vary")
	hdr.Add("Vary", "Origin")
}

// unmatchedPayload is the JSON body returned for requests with no recording.
type unmatchedPayload struct {
	Harmock     string             `json:"harmock"`
	Method      string             `json:"method"`
	Path        string             `json:"path"`
	Query       string             `json:"query,omitempty"`
	Suggestions []match.Suggestion `json:"suggestions,omitempty"`
}

func (h *Handler) writeUnmatched(w http.ResponseWriter, r *http.Request, sugg []match.Suggestion) {
	if h.cfg.CORS {
		h.writeCORS(w, r)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(h.cfg.FallbackStatus)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(unmatchedPayload{
		Harmock:     "no recorded entry matches this request",
		Method:      r.Method,
		Path:        r.URL.Path,
		Query:       r.URL.RawQuery,
		Suggestions: sugg,
	})
	h.cfg.Logf("<- %s %s %d (no match)", r.Method, r.URL.RequestURI(), h.cfg.FallbackStatus)
}

// sleepFor applies the configured latency simulation for route.
func (h *Handler) sleepFor(route *match.Route) {
	switch h.cfg.Latency {
	case LatencyRecord:
		ms := route.TimeMS
		if cap := float64(h.cfg.LatencyCapMS); ms > cap {
			ms = cap
		}
		if ms > 0 {
			h.cfg.Sleep(time.Duration(ms * float64(time.Millisecond)))
		}
	case LatencyFixed:
		if h.cfg.LatencyMS > 0 {
			h.cfg.Sleep(time.Duration(h.cfg.LatencyMS) * time.Millisecond)
		}
	}
}

// routeInfo is one row of GET /__harmock__/routes.
type routeInfo struct {
	Index     int    `json:"index"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Query     string `json:"query,omitempty"`
	Status    int    `json:"status"`
	MimeType  string `json:"mimeType,omitempty"`
	BodyBytes int    `json:"bodyBytes"`
}

// serveAdmin handles the /__harmock__/ endpoints.
func (h *Handler) serveAdmin(w http.ResponseWriter, r *http.Request) {
	writeJSON := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
	}
	switch {
	case r.URL.Path == AdminPrefix+"health" && r.Method == http.MethodGet:
		writeJSON(http.StatusOK, map[string]any{"ok": true, "routes": len(h.m.Routes())})
	case r.URL.Path == AdminPrefix+"routes" && r.Method == http.MethodGet:
		routes := h.m.Routes()
		infos := make([]routeInfo, len(routes))
		for i, rt := range routes {
			infos[i] = routeInfo{
				Index:     rt.Index,
				Method:    rt.Method,
				Path:      rt.Path,
				Query:     rt.Query.Encode(),
				Status:    rt.Status,
				MimeType:  rt.RespMimeType,
				BodyBytes: len(rt.RespBody),
			}
		}
		writeJSON(http.StatusOK, infos)
	case r.URL.Path == AdminPrefix+"reset" && r.Method == http.MethodPost:
		h.m.Reset()
		h.cfg.Logf("<- POST %sreset (cursors rewound)", AdminPrefix)
		writeJSON(http.StatusOK, map[string]any{"reset": true})
	default:
		writeJSON(http.StatusNotFound, map[string]any{
			"harmock": fmt.Sprintf("unknown admin endpoint; try GET %shealth, GET %sroutes, POST %sreset",
				AdminPrefix, AdminPrefix, AdminPrefix),
		})
	}
}
