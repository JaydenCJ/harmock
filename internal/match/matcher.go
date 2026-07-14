// Package match answers the core harmock question: which recorded HAR
// entry should serve this live request?
//
// Matching is score-based. Method and path must match exactly; query
// parameters, request body, and opted-in headers then rank the surviving
// candidates. Ties are broken by the replay strategy: `sequential`
// (default) walks duplicates in recorded order and sticks at the last one,
// `first`/`last` always pin one entry. Everything is deterministic —
// identical requests against an identical capture always pick the same
// entries in the same order.
package match

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Strategy selects among equally good candidates.
type Strategy string

const (
	// StrategySequential replays duplicate recordings in recorded order,
	// sticking at the last one once exhausted. The default.
	StrategySequential Strategy = "sequential"
	// StrategyFirst always serves the earliest matching recording.
	StrategyFirst Strategy = "first"
	// StrategyLast always serves the latest matching recording.
	StrategyLast Strategy = "last"
)

// ParseStrategy validates a --strategy flag value.
func ParseStrategy(s string) (Strategy, bool) {
	switch Strategy(s) {
	case StrategySequential, StrategyFirst, StrategyLast:
		return Strategy(s), true
	}
	return "", false
}

// BodyMode controls whether request bodies participate in scoring.
type BodyMode string

const (
	// BodyAuto compares bodies only when both sides have one. The default.
	BodyAuto BodyMode = "auto"
	// BodyAlways treats a body mismatch (including present vs absent) as
	// a strong negative signal.
	BodyAlways BodyMode = "always"
	// BodyNever ignores request bodies entirely.
	BodyNever BodyMode = "never"
)

// ParseBodyMode validates a --match-body flag value.
func ParseBodyMode(s string) (BodyMode, bool) {
	switch BodyMode(s) {
	case BodyAuto, BodyAlways, BodyNever:
		return BodyMode(s), true
	}
	return "", false
}

// Options tune the scoring model.
type Options struct {
	IgnoreQuery  []string // query keys excluded from comparison (cache busters, timestamps)
	MatchHeaders []string // request headers that participate in scoring
	MatchBody    BodyMode
}

// Incoming is a live request reduced to what matching needs.
type Incoming struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// Score components. Exact query equality dominates partial overlap, and an
// exact body match dominates query differences, so a POST with the recorded
// payload always beats a POST with a different one.
const (
	scoreQueryExact  = 100
	scoreQueryKeyEq  = 8
	scoreQueryKeyNeq = -4
	scoreQueryOnlyIn = -2
	scoreBodyExact   = 50
	scoreBodyJSONEq  = 40
	scoreBodyMissing = -25 // BodyAlways: one side has a body, the other does not
	scoreBodyDiff    = -10
	scoreHeaderEq    = 10
	scoreHeaderNeq   = -5
)

// Matcher holds the routes plus the per-key cursors used by sequential replay.
type Matcher struct {
	routes []Route
	opt    Options
	ignore map[string]bool

	mu      sync.Mutex
	cursors map[string]int
}

// New builds a Matcher over routes.
func New(routes []Route, opt Options) *Matcher {
	if opt.MatchBody == "" {
		opt.MatchBody = BodyAuto
	}
	ignore := make(map[string]bool, len(opt.IgnoreQuery))
	for _, k := range opt.IgnoreQuery {
		ignore[k] = true
	}
	return &Matcher{
		routes:  routes,
		opt:     opt,
		ignore:  ignore,
		cursors: make(map[string]int),
	}
}

// Routes returns the matcher's routes in recorded order.
func (m *Matcher) Routes() []Route { return m.routes }

// Reset clears all sequential-replay cursors, rewinding every duplicate
// group to its first recording. Exposed as POST /__harmock__/reset.
func (m *Matcher) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursors = make(map[string]int)
}

// filteredQuery returns q without the ignored keys, as a canonical string.
func (m *Matcher) filteredQuery(q url.Values) url.Values {
	if len(m.ignore) == 0 {
		return q
	}
	out := make(url.Values, len(q))
	for k, vs := range q {
		if !m.ignore[k] {
			out[k] = vs
		}
	}
	return out
}

// canonicalQuery renders values in a stable, order-independent form.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
			b.WriteByte('&')
		}
	}
	return b.String()
}

// jsonEqual reports whether a and b are the same JSON value regardless of
// key order or whitespace. Recorded fetch() bodies and hand-typed curl
// bodies rarely match byte-for-byte; structural equality catches that.
func jsonEqual(a, b []byte) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// scoreBody scores the request-body component.
func (m *Matcher) scoreBody(route Route, in Incoming) int {
	switch m.opt.MatchBody {
	case BodyNever:
		return 0
	case BodyAlways:
		if len(route.Body) == 0 && len(in.Body) == 0 {
			return 0
		}
		if len(route.Body) == 0 || len(in.Body) == 0 {
			return scoreBodyMissing
		}
	default: // BodyAuto: only compare when both sides carry a body
		if len(route.Body) == 0 || len(in.Body) == 0 {
			return 0
		}
	}
	if bytes.Equal(route.Body, in.Body) {
		return scoreBodyExact
	}
	if jsonEqual(route.Body, in.Body) {
		return scoreBodyJSONEq
	}
	return scoreBodyDiff
}

// scoreQuery scores the query-string component after ignore filtering.
func (m *Matcher) scoreQuery(route Route, in Incoming) int {
	rq := m.filteredQuery(route.Query)
	iq := m.filteredQuery(in.Query)
	if canonicalQuery(rq) == canonicalQuery(iq) {
		return scoreQueryExact
	}
	s := 0
	for k, iv := range iq {
		rv, ok := rq[k]
		if !ok {
			s += scoreQueryOnlyIn
			continue
		}
		if canonicalQuery(url.Values{k: iv}) == canonicalQuery(url.Values{k: rv}) {
			s += scoreQueryKeyEq
		} else {
			s += scoreQueryKeyNeq
		}
	}
	for k := range rq {
		if _, ok := iq[k]; !ok {
			s += scoreQueryOnlyIn
		}
	}
	return s
}

// scoreHeaders scores the opted-in header component.
func (m *Matcher) scoreHeaders(route Route, in Incoming) int {
	s := 0
	for _, name := range m.opt.MatchHeaders {
		rv := route.Header.Get(name)
		iv := in.Header.Get(name)
		if rv == "" && iv == "" {
			continue
		}
		if rv == iv {
			s += scoreHeaderEq
		} else {
			s += scoreHeaderNeq
		}
	}
	return s
}

// score computes the total score for route against in; ok is false when the
// route is not a candidate at all (method or path mismatch).
func (m *Matcher) score(route Route, in Incoming) (int, bool) {
	if route.Method != strings.ToUpper(in.Method) || route.Path != in.Path {
		return 0, false
	}
	return m.scoreQuery(route, in) + m.scoreBody(route, in) + m.scoreHeaders(route, in), true
}

// seqKey identifies a request for sequential-cursor purposes: method, path,
// and the canonical (ignore-filtered) query.
func (m *Matcher) seqKey(in Incoming) string {
	return strings.ToUpper(in.Method) + " " + in.Path + "?" + canonicalQuery(m.filteredQuery(in.Query))
}

// Match picks the route that should serve in, or nil when nothing matches.
// The returned slice holds near-miss suggestions for the 404 payload.
func (m *Matcher) Match(in Incoming, strategy Strategy) (*Route, []Suggestion) {
	best := -1 << 30
	var top []int // indices into m.routes with the best score, recorded order
	for i, r := range m.routes {
		s, ok := m.score(r, in)
		if !ok {
			continue
		}
		if s > best {
			best, top = s, top[:0]
		}
		if s == best {
			top = append(top, i)
		}
	}
	if len(top) == 0 {
		return nil, m.suggest(in)
	}

	var pick int
	switch strategy {
	case StrategyFirst:
		pick = top[0]
	case StrategyLast:
		pick = top[len(top)-1]
	default: // sequential
		key := m.seqKey(in)
		m.mu.Lock()
		cur := m.cursors[key]
		if cur >= len(top) {
			cur = len(top) - 1 // exhausted: stick at the last recording
		} else {
			m.cursors[key]++
		}
		m.mu.Unlock()
		pick = top[cur]
	}
	return &m.routes[pick], nil
}

// Suggestion is a near-miss route reported in the unmatched-request payload.
type Suggestion struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// suggest ranks near misses: same path with a different method first, then
// same-method paths sharing the longest common prefix. At most three.
func (m *Matcher) suggest(in Incoming) []Suggestion {
	method := strings.ToUpper(in.Method)
	type cand struct {
		s      Suggestion
		rank   int // lower is better
		prefix int // higher is better within a rank
		index  int
	}
	seen := make(map[string]bool)
	var cands []cand
	for _, r := range m.routes {
		key := r.Method + " " + r.Path
		if seen[key] {
			continue
		}
		switch {
		case r.Path == in.Path && r.Method != method:
			seen[key] = true
			cands = append(cands, cand{
				s:     Suggestion{r.Method, r.Path, "same path, different method"},
				rank:  0,
				index: r.Index,
			})
		case r.Method == method && r.Path != in.Path:
			p := commonPrefixLen(r.Path, in.Path)
			if p < 2 { // sharing only "/" is noise
				continue
			}
			seen[key] = true
			cands = append(cands, cand{
				s:      Suggestion{r.Method, r.Path, "similar path"},
				rank:   1,
				prefix: p,
				index:  r.Index,
			})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].rank != cands[j].rank {
			return cands[i].rank < cands[j].rank
		}
		if cands[i].prefix != cands[j].prefix {
			return cands[i].prefix > cands[j].prefix
		}
		return cands[i].index < cands[j].index
	})
	if len(cands) > 3 {
		cands = cands[:3]
	}
	out := make([]Suggestion, len(cands))
	for i, c := range cands {
		out[i] = c.s
	}
	return out
}

// commonPrefixLen returns the length of the shared byte prefix of a and b.
func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
