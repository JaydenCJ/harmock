// Package har parses HTTP Archive (HAR 1.2) captures — the JSON format
// exported by every browser's DevTools "Save all as HAR" — into typed
// entries with decoded request and response bodies.
//
// The parser is deliberately forgiving: real-world HAR files omit fields,
// carry vendor extensions, and contain aborted requests. Anything harmock
// cannot serve is reported as a Skip with a reason instead of failing the
// whole file.
package har

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// Archive is the top-level HAR document.
type Archive struct {
	Log Log `json:"log"`
}

// Log holds the capture metadata and the recorded entries.
type Log struct {
	Version string  `json:"version"`
	Creator Creator `json:"creator"`
	Entries []Entry `json:"entries"`
}

// Creator identifies the tool that produced the capture.
type Creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Entry is one recorded request/response exchange.
type Entry struct {
	StartedDateTime string   `json:"startedDateTime"`
	Time            float64  `json:"time"` // total elapsed milliseconds
	Request         Request  `json:"request"`
	Response        Response `json:"response"`
}

// Request is the recorded outgoing request.
type Request struct {
	Method      string    `json:"method"`
	URL         string    `json:"url"`
	HTTPVersion string    `json:"httpVersion"`
	Headers     []NVP     `json:"headers"`
	QueryString []NVP     `json:"queryString"`
	PostData    *PostData `json:"postData,omitempty"`
}

// Response is the recorded server response.
type Response struct {
	Status      int     `json:"status"`
	StatusText  string  `json:"statusText"`
	HTTPVersion string  `json:"httpVersion"`
	Headers     []NVP   `json:"headers"`
	Content     Content `json:"content"`
	RedirectURL string  `json:"redirectURL"`
}

// NVP is a HAR name/value pair (headers, query parameters, cookies).
type NVP struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PostData is the recorded request body.
type PostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	Params   []NVP  `json:"params,omitempty"`
}

// Content is the recorded response body. Text may be raw or base64-encoded
// (Encoding == "base64"), which is how DevTools stores binary payloads.
type Content struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	Encoding string `json:"encoding,omitempty"`
}

// Body returns the decoded response body bytes.
func (c Content) Body() ([]byte, error) {
	if c.Encoding == "base64" {
		// DevTools occasionally wraps base64 text; strip all whitespace first.
		compact := strings.Map(func(r rune) rune {
			switch r {
			case ' ', '\n', '\r', '\t':
				return -1
			}
			return r
		}, c.Text)
		b, err := base64.StdEncoding.DecodeString(compact)
		if err != nil {
			return nil, fmt.Errorf("base64 body: %w", err)
		}
		return b, nil
	}
	if c.Encoding != "" {
		return nil, fmt.Errorf("unsupported content encoding %q", c.Encoding)
	}
	return []byte(c.Text), nil
}

// Body returns the recorded request body bytes, or nil when none was captured.
func (r Request) Body() []byte {
	if r.PostData == nil || r.PostData.Text == "" {
		return nil
	}
	return []byte(r.PostData.Text)
}

// Started parses the entry's start timestamp. The zero time is returned for
// missing or malformed values so callers can fall back to file order.
func (e Entry) Started() time.Time {
	if e.StartedDateTime == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, e.StartedDateTime)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Parse reads a HAR document from r. It fails only on structural problems
// (not JSON, no log object); per-entry issues are left for BuildRoutes /
// check to report.
func Parse(r io.Reader) (*Archive, error) {
	dec := json.NewDecoder(r)
	var a Archive
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("not a HAR file: %w", err)
	}
	if a.Log.Entries == nil && a.Log.Version == "" && a.Log.Creator.Name == "" {
		return nil, fmt.Errorf("not a HAR file: missing log object")
	}
	return &a, nil
}

// ParseFile reads and parses the HAR document at path.
func ParseFile(path string) (*Archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	a, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

// SortEntries orders entries by startedDateTime, oldest first, using a
// stable sort so entries with equal or unparsable timestamps keep their
// file order. Recorded order is what sequential replay walks through.
func SortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		ti, tj := entries[i].Started(), entries[j].Started()
		if ti.IsZero() || tj.IsZero() {
			return false // keep file order when either timestamp is unusable
		}
		return ti.Before(tj)
	})
}
