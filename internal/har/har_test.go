// Tests for HAR parsing and body decoding. These pin the forgiving-parser
// contract: structural garbage fails loudly, per-entry damage does not.
package har

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMinimalArchive(t *testing.T) {
	a, err := Parse(strings.NewReader(`{"log":{"version":"1.2","entries":[]}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Log.Version != "1.2" || len(a.Log.Entries) != 0 {
		t.Fatalf("unexpected archive: %+v", a)
	}
}

func TestParseRejectsNonJSON(t *testing.T) {
	if _, err := Parse(strings.NewReader("<html>not har</html>")); err == nil {
		t.Fatal("want error for non-JSON input")
	}
}

func TestParseRejectsMissingLog(t *testing.T) {
	// Valid JSON that is clearly not a HAR document must not silently
	// become an empty archive — users would see "0 routes" with no clue.
	if _, err := Parse(strings.NewReader(`{"pets":[1,2,3]}`)); err == nil {
		t.Fatal("want error for JSON without a log object")
	}
}

func TestParseAcceptsCreatorOnlyLog(t *testing.T) {
	// Some exporters write creator before entries; a log with a creator but
	// null entries is still a HAR file (just an unusable one).
	a, err := Parse(strings.NewReader(`{"log":{"creator":{"name":"x","version":"1"}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Log.Creator.Name != "x" {
		t.Fatalf("creator not parsed: %+v", a.Log.Creator)
	}
}

func TestContentBodyDecoding(t *testing.T) {
	cases := []struct {
		name    string
		content Content
		want    string
		wantErr bool
	}{
		{"plain text passes through", Content{Text: `{"ok":true}`}, `{"ok":true}`, false},
		{"base64 is decoded", Content{Text: "aGVsbG8=", Encoding: "base64"}, "hello", false},
		// Some exporters wrap base64 at 76 columns; the decoder must cope.
		{"base64 with whitespace", Content{Text: "aGVs\nbG8=\n", Encoding: "base64"}, "hello", false},
		{"invalid base64 fails", Content{Text: "!!not-base64!!", Encoding: "base64"}, "", true},
		{"unknown encoding fails", Content{Text: "x", Encoding: "brotli"}, "", true},
	}
	for _, c := range cases {
		b, err := c.content.Body()
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want error, got %q", c.name, b)
			}
			continue
		}
		if err != nil || string(b) != c.want {
			t.Errorf("%s: got %q, %v", c.name, b, err)
		}
	}
}

func TestRequestBody(t *testing.T) {
	if b := (Request{}).Body(); b != nil {
		t.Fatalf("want nil body without postData, got %q", b)
	}
	r := Request{PostData: &PostData{MimeType: "application/json", Text: `{"a":1}`}}
	if string(r.Body()) != `{"a":1}` {
		t.Fatalf("got %q", r.Body())
	}
}

func TestStartedTimestampParsing(t *testing.T) {
	e := Entry{StartedDateTime: "2026-07-01T09:00:05.250Z"}
	if got := e.Started(); got.IsZero() || got.Second() != 5 {
		t.Fatalf("Started = %v", got)
	}
	for _, s := range []string{"", "yesterday", "2026-13-99"} {
		if got := (Entry{StartedDateTime: s}).Started(); !got.IsZero() {
			t.Fatalf("Started(%q) = %v, want zero", s, got)
		}
	}
}

func TestSortEntriesByTimestamp(t *testing.T) {
	entries := []Entry{
		{StartedDateTime: "2026-07-01T09:00:02Z", Time: 2},
		{StartedDateTime: "2026-07-01T09:00:00Z", Time: 0},
		{StartedDateTime: "2026-07-01T09:00:01Z", Time: 1},
	}
	SortEntries(entries)
	for i, e := range entries {
		if e.Time != float64(i) {
			t.Fatalf("position %d holds entry %v", i, e.Time)
		}
	}
}

func TestSortEntriesKeepsFileOrderOnBadTimestamps(t *testing.T) {
	// Unparsable timestamps must not shuffle entries: file order is the
	// only remaining signal for sequential replay.
	entries := []Entry{
		{StartedDateTime: "garbage", Time: 0},
		{StartedDateTime: "", Time: 1},
		{StartedDateTime: "also-garbage", Time: 2},
	}
	SortEntries(entries)
	for i, e := range entries {
		if e.Time != float64(i) {
			t.Fatalf("position %d holds entry %v (order not preserved)", i, e.Time)
		}
	}
}

func TestParseFileMissing(t *testing.T) {
	if _, err := ParseFile(filepath.Join(t.TempDir(), "nope.har")); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestParseFileErrorMentionsPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.har")
	if err := os.WriteFile(p, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseFile(p)
	if err == nil || !strings.Contains(err.Error(), "broken.har") {
		t.Fatalf("error should name the file: %v", err)
	}
}

func TestParseExampleCapture(t *testing.T) {
	a, err := ParseFile(filepath.Join("..", "..", "examples", "petstore.har"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(a.Log.Entries) != 9 {
		t.Fatalf("example capture has %d entries, want 9", len(a.Log.Entries))
	}
	if a.Log.Version != "1.2" {
		t.Fatalf("version = %q", a.Log.Version)
	}
}
