package tilelibrary_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// fakeChecks is a tilelibrary.Checks over results the test wrote.
type fakeChecks map[string]tilelibrary.CheckResult

func (f fakeChecks) Result(_ context.Context, id string) (tilelibrary.CheckResult, bool) {
	r, ok := f[id]
	return r, ok
}

// passing is a check that passed on every sample of c, found synthetic.
func passing(t testing.TB, c tilelibrary.Change) tilelibrary.CheckResult {
	t.Helper()
	var def tiles.TypeDefinition
	if err := json.Unmarshal(c.Definition, &def); err != nil {
		t.Fatal(err)
	}
	r := tilelibrary.CheckResult{ID: "chk_ok", Verdict: tilelibrary.VerdictPass, Digest: def.Digest, Definition: c.Definition}
	for _, s := range c.Samples {
		r.Samples = append(r.Samples, tilelibrary.CheckedSample{Sample: s, Passed: true, Synthetic: true})
	}
	return r
}

func str(s string) *string { return &s }

func TestProposeFromACheck(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	c := change(t, "env_vars", "")
	checks := fakeChecks{"chk_ok": passing(t, c)}

	got, err := s.Propose(ctx, checks, tilelibrary.Request{Check: "chk_ok", Glance: str("New variables with their defaults."), UseWhen: str("When env vars change. Not tests."), Title: str("Environment variables")})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := tilelibrary.Prepare(got)
	if err != nil {
		t.Fatal(err)
	}
	def := prepared.Entry.Definition
	if def.Name != "env_vars" || def.Title != "Environment variables" || def.UseWhen != "When env vars change. Not tests." || got.Origin != "chat" {
		t.Errorf("proposal: %+v", def)
	}
	var checked tiles.TypeDefinition
	_ = json.Unmarshal(c.Definition, &checked)
	if def.Source != checked.Source || string(def.DataSchema) != string(checked.DataSchema) || def.Fill != checked.Fill {
		t.Error("the proposal changed the component, schema or fill the check ran")
	}
	// Nothing is saved until the person saves.
	if list, _ := s.List(ctx, true); len(list) != 0 {
		t.Error("a proposal saved a type")
	}
	saved, err := s.Create(ctx, got, nil)
	if err != nil || saved.Entry.Definition.Digest != def.Digest {
		t.Fatalf("save: %v", err)
	}
}

// Only samples that passed and are synthetic are kept; with none a proposal
// is refused.
func TestProposeKeepsOnlyPassedSyntheticSamples(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	c := change(t, "env_vars", "")
	result := passing(t, c)
	n := len(result.Samples)
	result.Samples = append(result.Samples,
		tilelibrary.CheckedSample{Sample: library.Sample{Name: "failed", Data: c.Samples[0].Data, Hunks: c.Samples[0].Hunks, Items: c.Samples[0].Items}, Passed: false, Synthetic: true},
		tilelibrary.CheckedSample{Sample: library.Sample{Name: "real", Data: c.Samples[0].Data}, Passed: true, Synthetic: false},
		tilelibrary.CheckedSample{Sample: library.Sample{Name: "claims", Data: c.Samples[0].Data}, Passed: true, Synthetic: true})
	got, err := s.Propose(ctx, fakeChecks{"chk_ok": result}, tilelibrary.Request{Check: "chk_ok", Glance: str("A glance.")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Samples) != n {
		t.Errorf("%d samples kept, want %d", len(got.Samples), n)
	}
	for _, sample := range got.Samples {
		if sample.Name == "failed" || sample.Name == "real" || sample.Name == "claims" {
			t.Errorf("kept %s", sample.Name)
		}
	}
	for _, only := range [][]tilelibrary.CheckedSample{
		{{Sample: c.Samples[0], Passed: true, Synthetic: false}},
		{{Sample: c.Samples[0], Passed: false, Synthetic: true}},
		nil,
	} {
		r := passing(t, c)
		r.Samples = only
		if _, err := s.Propose(ctx, fakeChecks{"chk_ok": r}, tilelibrary.Request{Check: "chk_ok"}); err == nil || !strings.Contains(err.Error(), "synthetic") {
			t.Errorf("a check with %d samples: %v", len(only), err)
		}
	}
}

func TestProposeRefusals(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	c := change(t, "env_vars", "")
	ok := passing(t, c)
	failed := ok
	failed.Verdict = "fail"
	mismatch := ok
	mismatch.Digest = "sha256:" + strings.Repeat("0", 64)
	checks := fakeChecks{"chk_ok": ok, "chk_fail": failed, "chk_digest": mismatch}
	mustCreate(t, s, change(t, "taken", ""))
	mustCreate(t, s, change(t, "gone", ""))
	if err := s.Delete(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		req  tilelibrary.Request
		want string
	}{
		{"unknown check", tilelibrary.Request{Check: "chk_none"}, "unknown or has expired"},
		{"failed check", tilelibrary.Request{Check: "chk_fail"}, "did not pass"},
		{"digest mismatch", tilelibrary.Request{Check: "chk_digest"}, "digest"},
		{"built-in name", tilelibrary.Request{Check: "chk_ok", Name: str("schema_change")}, "baum's own"},
		{"reserved name", tilelibrary.Request{Check: "chk_ok", Name: str("sec_x")}, "cannot start"},
		{"custom", tilelibrary.Request{Check: "chk_ok", Name: str("custom")}, "custom"},
		{"taken name from a tile", tilelibrary.Request{Check: "chk_ok", Name: str("taken")}, "already in your library"},
		{"deleted name", tilelibrary.Request{Check: "chk_ok", Name: str("gone")}, "deleted"},
		{"unknown source", tilelibrary.Request{FromName: "nothing", Check: "chk_ok"}, "no tile type"},
		{"built-in edited in place", tilelibrary.Request{FromName: "schema_change", Check: "chk_ok", Name: str("schema_change")}, "baum's own"},
		{"no glance", tilelibrary.Request{Check: "chk_ok", Glance: str("")}, "glance"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Propose(ctx, checks, tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := s.Propose(ctx, nil, tilelibrary.Request{Check: "chk_ok"}); err == nil {
		t.Error("no checker")
	}
}

// Starting from a saved type edits it (the check's definition becomes a new
// version and the type's texts stay); starting from a built-in type makes a
// copy under a name of its own.
func TestProposeFromATypeEditsOrCopies(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	saved := mustCreate(t, s, change(t, "migrations", ""))
	edited := change(t, "migrations", " Also indexes.")
	checks := fakeChecks{"chk_ok": passing(t, edited)}
	got, err := s.Propose(ctx, checks, tilelibrary.Request{FromName: "migrations", Check: "chk_ok", ReviewChecks: str("Check the rollback.")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Glance != saved.Entry.Glance || got.ReviewChecks != "Check the rollback." || len(got.Paths) != 1 || got.Paths[0] != "**/*.sql" {
		t.Errorf("an edit lost the type's texts: %+v", got)
	}
	after, err := s.Update(ctx, got, func(st tilelibrary.State) error {
		if st.Digest != saved.Entry.Definition.Digest {
			return tilelibrary.ErrStale
		}
		return nil
	})
	if err != nil || len(after.Versions) != 2 {
		t.Fatalf("update: %v", err)
	}

	copied, err := s.Propose(ctx, checks, tilelibrary.Request{FromName: "schema_change", Name: str("my_schema"), Check: "chk_ok"})
	if err != nil {
		t.Fatal(err)
	}
	builtin, _ := library.Get("schema_change")
	if copied.Glance != builtin.Glance || copied.ReviewChecks != builtin.ReviewChecks {
		t.Error("a copy of a built-in type lost its texts")
	}
	prepared, _ := tilelibrary.Prepare(copied)
	if prepared.Name() != "my_schema" || library.IsBuiltin(prepared.Entry.Encoded) {
		t.Errorf("copy: %s", prepared.Name())
	}
	if _, err := s.Create(ctx, copied, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, copied, nil); !errors.Is(err, tilelibrary.ErrExists) {
		t.Error(err)
	}
}

// A sample whose hunks share three or more non-trivial lines with the open
// pull request's diff is a copy of its code and is refused; fewer lines, or
// trivial ones, are not; with no pull request page there is nothing to
// compare with.
func TestProposeRefusesSamplesCopiedFromThePullRequest(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	c := change(t, "env_vars", "")
	checks := fakeChecks{"chk_ok": passing(t, c)}
	var shared []string
	for _, h := range c.Samples[0].Hunks {
		for _, l := range h.Lines {
			if strings.HasPrefix(l.Text, "CREATE TABLE") && !slices.Contains(shared, l.Text) {
				shared = append(shared, l.Text)
			}
		}
	}
	if len(shared) < 3 {
		t.Fatalf("the sample has %d table lines", len(shared))
	}
	diff := func(lines ...string) func(context.Context) ([]string, error) {
		return func(context.Context) ([]string, error) { return lines, nil }
	}
	req := func(f func(context.Context) ([]string, error)) tilelibrary.Request {
		return tilelibrary.Request{Check: "chk_ok", Glance: str("A glance."), PullDiff: f}
	}
	trivial := []string{"", "}", "  });", "import \"os\"", "package main", "if err != nil {", "return nil, err", "x := 1"}

	for name, f := range map[string]func(context.Context) ([]string, error){
		"no pull request":                nil,
		"two shared lines and trivial":   diff(append([]string{"  " + shared[0], shared[1] + " "}, trivial...)...),
		"the same line three times":      diff(shared[0], shared[0], shared[0]),
		"trivial lines of the sample":    diff(trivial...),
		"lines of another pull request":  diff("func unrelatedFunction() {", "\treturn unrelatedValue()", "}"),
		"a diff with a patch of nothing": diff(),
	} {
		if _, err := s.Propose(ctx, checks, req(f)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := s.Propose(ctx, checks, req(diff("\t"+shared[0], shared[1], shared[2]+"  ")))
	if err == nil || !strings.Contains(err.Error(), "fake repository") || strings.Contains(err.Error(), shared[0]) {
		t.Errorf("a copy: %v", err)
	}
	failing := func(context.Context) ([]string, error) { return nil, errors.New("no diff") }
	if _, err := s.Propose(ctx, checks, req(failing)); err == nil || !strings.Contains(err.Error(), "diff") {
		t.Errorf("a diff that cannot be read: %v", err)
	}
}
