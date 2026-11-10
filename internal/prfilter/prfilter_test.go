package prfilter

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/baum-ai/baum/internal/reviewindex"
)

type fixtures struct {
	Entries map[string]json.RawMessage `json:"entries"`
	Queries []struct {
		Query   string   `json:"query"`
		Kind    string   `json:"kind"`
		Error   string   `json:"error"`
		Matches []string `json:"matches"`
	} `json:"queries"`
	Problems []struct {
		Query   string `json:"query"`
		Problem string `json:"problem"`
	} `json:"problems"`
	Folded []struct {
		Inbox struct {
			What         string   `json:"what"`
			Owners       []string `json:"owners"`
			Risk         string   `json:"risk"`
			Repositories []string `json:"repositories"`
			Query        string   `json:"query"`
			NoBots       bool     `json:"no_bots"`
		} `json:"inbox"`
		Query string `json:"query"`
	} `json:"folded"`
}

func load(t *testing.T) (fixtures, []string, map[string]reviewindex.Entry) {
	t.Helper()
	data, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	entries := map[string]reviewindex.Entry{}
	var names []string
	for name, raw := range f.Entries {
		var e reviewindex.Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("entry %s: %v", name, err)
		}
		entries[name] = e
		names = append(names, name)
	}
	sort.Strings(names)
	return f, names, entries
}

func TestFixtureQueries(t *testing.T) {
	f, names, entries := load(t)
	if len(f.Queries) < 80 {
		t.Fatalf("only %d query cases", len(f.Queries))
	}
	for _, c := range f.Queries {
		_, err := Parse(c.Query)
		switch {
		case c.Error == "" && err != nil:
			t.Errorf("%q: unexpected error %q", c.Query, err)
		case c.Error != "" && err == nil:
			t.Errorf("%q: want error %q, parsed", c.Query, c.Error)
		case err != nil && err.Error() != c.Error:
			t.Errorf("%q: error %q, want %q", c.Query, err, c.Error)
		}
		matcher := Compile(c.Query)
		got := []string{}
		for _, name := range names {
			e := entries[name]
			one := Matches(c.Query, e, c.Kind)
			if one != matcher(e, c.Kind) {
				t.Errorf("%q: Compile and Matches disagree on %s", c.Query, name)
			}
			if one {
				got = append(got, name)
			}
		}
		want := c.Matches
		if want == nil {
			want = []string{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q (kind %q): matches %v, want %v", c.Query, c.Kind, got, want)
		}
	}
}

func TestFixtureProblems(t *testing.T) {
	f, _, _ := load(t)
	for _, c := range f.Problems {
		if got := Problem(c.Query); got != c.Problem {
			t.Errorf("%.40q: problem %q, want %q", c.Query, got, c.Problem)
		}
	}
}

func TestFixtureFolded(t *testing.T) {
	f, _, _ := load(t)
	for _, c := range f.Folded {
		i := c.Inbox
		got := FoldedQuery(Parts{What: i.What, Owners: i.Owners, Risk: i.Risk, Repositories: i.Repositories, Query: i.Query, NoBots: i.NoBots})
		if got != c.Query {
			t.Errorf("%+v: folded %q, want %q", i, got, c.Query)
		}
	}
}

func TestEmptyQueryParsesToNothing(t *testing.T) {
	for _, q := range []string{"", "  \t\n", "\u00a0\ufeff"} {
		expr, err := Parse(q)
		if expr != nil || err != nil {
			t.Errorf("%q: got %v, %v", q, expr, err)
		}
	}
}

func TestParseShape(t *testing.T) {
	expr, err := Parse("a b OR (c d)")
	if err != nil || expr.Kind != KindOr || len(expr.Items) != 2 {
		t.Fatalf("got %+v, %v", expr, err)
	}
	if expr.Items[0].Kind != KindAnd || expr.Items[1].Kind != KindAnd || !expr.Items[1].Grouped || expr.Items[0].Grouped {
		t.Errorf("unexpected shape %+v", expr)
	}
	term, _ := Parse("risk:>=3")
	if term.Kind != KindTerm || term.Key != "risk" || term.Op != ">=" || !reflect.DeepEqual(term.Values, []string{"3"}) {
		t.Errorf("unexpected term %+v", term)
	}
}

func TestParseIsCachedAndBounded(t *testing.T) {
	a, _ := Parse("flaky")
	b, _ := Parse("flaky")
	if a != b {
		t.Error("the same query parsed twice")
	}
	for i := 0; i < 1000; i++ {
		_, _ = Parse("word" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)))
	}
	cache.Lock()
	n := len(cache.m)
	cache.Unlock()
	if n > 201 {
		t.Errorf("cache holds %d queries", n)
	}
}

func TestEntryDecodesRiskScoreShapes(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"score":3}`: true, `{"score":null}`: false, `null`: false, ``: false, `{}`: false, `"x"`: false,
	} {
		e := reviewindex.Entry{RiskScore: json.RawMessage(raw)}
		if _, ok := score(e); ok != want {
			t.Errorf("risk_score %q: scored %v, want %v", raw, ok, want)
		}
	}
}
