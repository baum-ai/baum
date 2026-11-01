package locallink

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// A document without its hunks' text, as the store keeps it, gets the text
// back from the clone, masked as the stored hunks are, with each hunk's
// github_url derived; when the clone lacks the commits, the hunks keep no
// text and only the derived fragment is added.
func TestEmbedPatchesMasksTheStoredHunksText(t *testing.T) {
	f := linktest.New(t)
	lines := strings.Split(strings.TrimSuffix(numbered(30), "\n"), "\n")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")
	writeIn(t, f, "config.go", strings.Join(lines, "\n")+"\n")
	writeIn(t, f, "old.go", "token = \""+patchSecret+"\"\n")
	linktest.Git(t, f.Repo, "add", "-A")
	linktest.Git(t, f.Repo, "commit", "-q", "-m", "Config")
	base := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	linktest.Git(t, f.Repo, "checkout", "-q", "-b", "secrets")
	edited := append([]string(nil), lines...)
	edited[2] = "key = \"" + patchSecret + "\""
	edited[25] = "owner = dev@example.com"
	writeIn(t, f, "config.go", strings.Join(edited, "\n")+"\n")
	linktest.Git(t, f.Repo, "rm", "-q", "old.go")
	linktest.Git(t, f.Repo, "commit", "-q", "-am", "Secrets")
	head := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")

	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	var stored []bundle.Hunk
	for path, status := range map[string]string{"config.go": "modified", "old.go": "removed"} {
		raw := linktest.Git(t, f.Repo, "diff", "--no-color", "--unified=3", base, head, "--", path) + "\n"
		hunks, _, err := bundle.BuildDiffIndexWithin(bundle.Comparison{Files: []bundle.FileChange{{NewPath: path, Status: status}}},
			func(...string) ([]byte, error) { return []byte(raw), nil }, bundle.RoleForPath, bundle.DiffLimits{})
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, hunks...)
	}
	redacted := &bundle.Bundle{Diff: stored}
	if err := bundle.Redact(context.Background(), redacted, redactor); err != nil {
		t.Fatal(err)
	}
	document := func(head string) []byte {
		var hunks []string
		for _, h := range redacted.Diff {
			hunks = append(hunks, fmt.Sprintf(`{"id":%q,"path":%q,"change":%q,"role":%q,"new_range":[%d,%d],"additions":%d,"deletions":%d}`,
				h.ID, h.Path, h.Change, h.Role, h.StartLine, h.LineCount, h.Additions, h.Deletions))
		}
		return fmt.Appendf(nil, `{"review":{"repository":%q,"head":%q,"base":%q,"merge_base":%q},"index":{"hunks":[%s]}}`,
			linktest.Repository, head, base, base, strings.Join(hunks, ","))
	}
	type line struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	type embedded struct {
		Index struct {
			Hunks []struct {
				ID        string `json:"id"`
				GitHubURL string `json:"github_url"`
				Diff      *struct {
					Header string `json:"header"`
					Lines  []line `json:"lines"`
				} `json:"diff"`
			} `json:"hunks"`
		} `json:"index"`
	}
	read := func(raw []byte) embedded {
		t.Helper()
		var out embedded
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Index.Hunks) != len(redacted.Diff) || len(redacted.Diff) != 3 {
			t.Fatalf("%d hunks, want the %d stored", len(out.Index.Hunks), len(redacted.Diff))
		}
		for i, hunk := range out.Index.Hunks {
			if want := redacted.Diff[i].DiffURL; hunk.GitHubURL != want {
				t.Errorf("%s: github_url %q, want %q", hunk.ID, hunk.GitHubURL, want)
			}
		}
		return out
	}

	out, err := EmbedPatches(context.Background(), f.Repo, document(head), 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), patchSecret) || strings.Contains(string(out), "dev@example.com") {
		t.Fatalf("a secret survives:\n%s", out)
	}
	for i, hunk := range read(out).Index.Hunks {
		if hunk.Diff == nil {
			t.Fatalf("%s: no text embedded", hunk.ID)
		}
		var text strings.Builder
		text.WriteString(hunk.Diff.Header + "\n")
		for _, l := range hunk.Diff.Lines {
			text.WriteString(map[string]string{"add": "+", "remove": "-", "context": " "}[l.Kind] + l.Text + "\n")
		}
		if want := redacted.Diff[i].Patch + "\n"; text.String() != want {
			t.Errorf("%s: embedded\n%s\nwant the stored hunk\n%s", hunk.ID, text.String(), want)
		}
	}

	missing := strings.Repeat("0", 40)
	out, err = EmbedPatches(context.Background(), f.Repo, document(missing), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, hunk := range read(out).Index.Hunks {
		if hunk.Diff != nil {
			t.Errorf("%s: text embedded for a head the clone lacks", hunk.ID)
		}
	}
}
