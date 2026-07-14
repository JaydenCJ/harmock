// Check report: lints a HAR capture for everything that will surprise a
// user at serve time, before they start the server.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/JaydenCJ/harmock/internal/har"
	"github.com/JaydenCJ/harmock/internal/match"
)

// Finding is one check result. Level is "error", "warning", or "info".
type Finding struct {
	Level   string `json:"level"`
	Entry   int    `json:"entry"` // -1 for archive-level findings
	Message string `json:"message"`
}

// CheckReport summarizes a capture's serve-readiness.
type CheckReport struct {
	Entries  int       `json:"entries"`
	Servable int       `json:"servable"`
	Skipped  int       `json:"skipped"`
	Findings []Finding `json:"findings"`
}

// Errors reports whether any finding is error-level (exit code 1).
func (r CheckReport) Errors() bool {
	for _, f := range r.Findings {
		if f.Level == "error" {
			return true
		}
	}
	return false
}

// Check lints archive against the routes/skips BuildRoutes produced from it.
func Check(a *har.Archive, routes []match.Route, skips []match.Skip) CheckReport {
	rep := CheckReport{
		Entries:  len(a.Log.Entries),
		Servable: len(routes),
		Skipped:  len(skips),
	}
	add := func(level string, entry int, format string, args ...any) {
		rep.Findings = append(rep.Findings, Finding{level, entry, fmt.Sprintf(format, args...)})
	}

	if a.Log.Version != "" && !strings.HasPrefix(a.Log.Version, "1.") {
		add("warning", -1, "HAR version %q is untested; harmock targets 1.x", a.Log.Version)
	}
	if len(a.Log.Entries) == 0 {
		add("error", -1, "capture contains no entries")
	}

	for _, s := range skips {
		level := "warning"
		if strings.HasPrefix(s.Reason, "undecodable") || strings.HasPrefix(s.Reason, "unparsable") {
			level = "error"
		}
		add(level, s.Index, "skipped: %s", s.Reason)
	}
	if len(a.Log.Entries) > 0 && len(routes) == 0 {
		add("error", -1, "no entry is servable")
	}

	// Truncated exports: DevTools sometimes writes size > 0 with empty text
	// when "Save all as HAR with content" was not used.
	for _, r := range routes {
		if len(r.RespBody) == 0 && bodyExpected(a, r) {
			add("warning", r.Index,
				"response body missing (%s %s) — re-export with content included", r.Method, r.Path)
		}
	}

	// Duplicate request lines: informational, they drive sequential replay.
	counts := make(map[string]int)
	order := make([]string, 0)
	for _, r := range routes {
		key := r.Method + " " + r.Path + "?" + r.Query.Encode()
		if counts[key] == 0 {
			order = append(order, key)
		}
		counts[key]++
	}
	for _, key := range order {
		if n := counts[key]; n > 1 {
			add("info", -1, "%s recorded %d times — served in order under --strategy sequential",
				strings.TrimSuffix(key, "?"), n)
		}
	}
	return rep
}

// bodyExpected reports whether the recorded response should have carried a
// body: content.size claims bytes, and the status is not bodyless.
func bodyExpected(a *har.Archive, r match.Route) bool {
	if r.Status == 204 || r.Status == 304 || (r.Status >= 100 && r.Status < 200) {
		return false
	}
	for _, e := range a.Log.Entries {
		// Compare against the URL's path only — a query string or fragment
		// would defeat the suffix match. Suffix (not equality) keeps this
		// working when routes were rewritten by --strip-prefix.
		u := e.Request.URL
		if i := strings.IndexAny(u, "?#"); i >= 0 {
			u = u[:i]
		}
		if strings.EqualFold(e.Request.Method, r.Method) && strings.HasSuffix(u, r.Path) &&
			e.Response.Status == r.Status && e.Response.Content.Size > 0 && e.Response.Content.Text == "" {
			return true
		}
	}
	return false
}

// CheckText writes the human-readable report.
func CheckText(w io.Writer, path string, rep CheckReport) {
	fmt.Fprintf(w, "harmock check — %s\n", path)
	fmt.Fprintf(w, "entries: %d   servable: %d   skipped: %d\n", rep.Entries, rep.Servable, rep.Skipped)
	if len(rep.Findings) == 0 {
		fmt.Fprintln(w, "no findings — capture is ready to serve")
		return
	}
	fmt.Fprintln(w)
	for _, f := range rep.Findings {
		loc := "archive"
		if f.Entry >= 0 {
			loc = fmt.Sprintf("entry %d", f.Entry)
		}
		fmt.Fprintf(w, "%-7s  %-8s  %s\n", f.Level, loc, f.Message)
	}
}

// CheckJSON writes the machine-readable report.
func CheckJSON(w io.Writer, rep CheckReport) error {
	if rep.Findings == nil {
		rep.Findings = []Finding{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
