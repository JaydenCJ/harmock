// Package render formats routes, entry details, and check reports for the
// terminal and for machines. All output is deterministic: same capture,
// same bytes.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/JaydenCJ/harmock/internal/match"
)

// shortMime compresses a MIME type for the routes table.
func shortMime(m string) string {
	if m == "" {
		return "-"
	}
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = m[:i]
	}
	if i := strings.IndexByte(m, '/'); i >= 0 {
		sub := m[i+1:]
		if j := strings.IndexByte(sub, '+'); j >= 0 {
			sub = sub[j+1:] // "vnd.api+json" -> "json"
		}
		return sub
	}
	return m
}

// humanSize renders a byte count compactly (0-padded style avoided; stable).
func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fkB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// RoutesText writes the aligned routes table. Duplicate (method, path,
// query) groups are annotated with their replay position so users can see
// sequential replay at a glance.
func RoutesText(w io.Writer, routes []match.Route) {
	if len(routes) == 0 {
		fmt.Fprintln(w, "no servable routes")
		return
	}
	// Count duplicates per request line.
	counts := make(map[string]int)
	for _, r := range routes {
		counts[r.Method+" "+r.Path+"?"+r.Query.Encode()]++
	}
	seen := make(map[string]int)

	type row struct{ idx, method, urlish, status, typ, size, note string }
	rows := make([]row, 0, len(routes))
	for _, r := range routes {
		urlish := r.Path
		if q := r.Query.Encode(); q != "" {
			urlish += "?" + q
		}
		key := r.Method + " " + r.Path + "?" + r.Query.Encode()
		note := ""
		if counts[key] > 1 {
			seen[key]++
			note = fmt.Sprintf("replay %d/%d", seen[key], counts[key])
		}
		rows = append(rows, row{
			idx:    fmt.Sprintf("#%d", r.Index),
			method: r.Method,
			urlish: urlish,
			status: fmt.Sprintf("%d", r.Status),
			typ:    shortMime(r.RespMimeType),
			size:   humanSize(len(r.RespBody)),
			note:   note,
		})
	}
	wIdx, wMeth, wURL, wStat, wTyp, wSize := 2, 6, 4, 3, 4, 4
	for _, r := range rows {
		wIdx = maxInt(wIdx, len(r.idx))
		wMeth = maxInt(wMeth, len(r.method))
		wURL = maxInt(wURL, len(r.urlish))
		wStat = maxInt(wStat, len(r.status))
		wTyp = maxInt(wTyp, len(r.typ))
		wSize = maxInt(wSize, len(r.size))
	}
	// TrimRight keeps rows with an empty NOTE free of trailing whitespace.
	line := fmt.Sprintf("%-*s  %-*s  %-*s  %*s  %-*s  %*s  %s",
		wIdx, "#", wMeth, "METHOD", wURL, "PATH", wStat, "ST", wTyp, "TYPE", wSize, "SIZE", "NOTE")
	fmt.Fprintln(w, strings.TrimRight(line, " "))
	for _, r := range rows {
		line = fmt.Sprintf("%-*s  %-*s  %-*s  %*s  %-*s  %*s  %s",
			wIdx, r.idx, wMeth, r.method, wURL, r.urlish, wStat, r.status, wTyp, r.typ, wSize, r.size, r.note)
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// routeJSON mirrors the admin /__harmock__/routes shape.
type routeJSON struct {
	Index     int    `json:"index"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Query     string `json:"query,omitempty"`
	Status    int    `json:"status"`
	MimeType  string `json:"mimeType,omitempty"`
	BodyBytes int    `json:"bodyBytes"`
}

// RoutesJSON writes the routes list as indented JSON.
func RoutesJSON(w io.Writer, routes []match.Route) error {
	out := make([]routeJSON, len(routes))
	for i, r := range routes {
		out[i] = routeJSON{
			Index:     r.Index,
			Method:    r.Method,
			Path:      r.Path,
			Query:     r.Query.Encode(),
			Status:    r.Status,
			MimeType:  r.RespMimeType,
			BodyBytes: len(r.RespBody),
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// previewLimit bounds body previews in `harmock show`.
const previewLimit = 2048

// bodyPreview renders body for the terminal: printable text is shown
// (truncated), binary is summarized.
func bodyPreview(body []byte) string {
	if len(body) == 0 {
		return "(empty)"
	}
	printable := true
	for _, r := range string(body[:minInt(len(body), 512)]) {
		if r == unicode.ReplacementChar || (!unicode.IsPrint(r) && !unicode.IsSpace(r)) {
			printable = false
			break
		}
	}
	if !printable {
		return fmt.Sprintf("(binary, %s)", humanSize(len(body)))
	}
	s := string(body)
	if len(s) > previewLimit {
		return s[:previewLimit] + fmt.Sprintf("\n… (%s total)", humanSize(len(body)))
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sortedHeaderLines renders an http.Header deterministically.
func sortedHeaderLines(h map[string][]string) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		for _, v := range h[k] {
			lines = append(lines, fmt.Sprintf("  %s: %s", k, v))
		}
	}
	return lines
}

// ShowText writes the full detail of one route: request line, recorded
// headers, bodies, and the response that will be replayed.
func ShowText(w io.Writer, r match.Route) {
	urlish := r.Path
	if q := r.Query.Encode(); q != "" {
		urlish += "?" + q
	}
	fmt.Fprintf(w, "entry #%d\n\n", r.Index)
	fmt.Fprintf(w, "request  %s %s\n", r.Method, urlish)
	fmt.Fprintf(w, "host     %s\n", r.Host)
	for _, l := range sortedHeaderLines(r.Header) {
		fmt.Fprintln(w, l)
	}
	if len(r.Body) > 0 {
		fmt.Fprintf(w, "\nrequest body (%s):\n%s\n", humanSize(len(r.Body)), bodyPreview(r.Body))
	}
	fmt.Fprintf(w, "\nresponse %d  %s  %s  (%.0f ms recorded)\n",
		r.Status, shortMime(r.RespMimeType), humanSize(len(r.RespBody)), r.TimeMS)
	for _, l := range sortedHeaderLines(r.RespHeader) {
		fmt.Fprintln(w, l)
	}
	fmt.Fprintf(w, "\nresponse body:\n%s\n", bodyPreview(r.RespBody))
}
