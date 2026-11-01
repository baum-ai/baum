package doctor

import (
	"net/url"
	"strings"
	"testing"
)

// Synthetic secrets of the shapes baum meets. None may reach a public issue.
var leakyValues = []string{
	"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
	"gho_PLANTEDkeyringTOKEN000000000000000000",
	"github_pat_11ABCDEFG0abcdefghijkl_mnopqrstuvwxyz0123456789ABCDEFGH",
	"sk-ant-api03-PLANTEDapikey000000000000000000000",
	"sk-proj-abcdefghijklmnop1234",
	"xoxb-1234567890-abcdefghij",
	"AKIAIOSFODNN7EXAMPLE",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r",
	"c0ffee1234567890abcdef1234567890abcdef12",
	"hunter2secretvalue",
	"ada.lovelace@example.com",
}

func hostile() Report {
	return Report{Status: StatusProblem, Checks: []Check{
		{ID: "git", Label: "git", Status: StatusProblem, Path: "/home/ada/bin/git",
			Detail: "running /home/ada/bin/git --version failed: exit status 1: " + leakyValues[0]},
		{ID: "claude", Label: "claude", Status: StatusWarning, Version: "2.1.7",
			Detail: "failed\nANTHROPIC_API_KEY=" + leakyValues[3] + "\nAuthorization: Bearer " + leakyValues[9] + "\ncontact " + leakyValues[10]},
		{ID: "config", Label: "config file", Status: StatusProblem, Path: "/home/ada/.config/baum/config.toml",
			Detail: `the configuration does not load: toml: line 3: token = "` + leakyValues[9] + `"`,
			Fix:    "fix " + leakyValues[2]},
		{ID: "store-abc", Label: "reviews of acme/secret-project", Status: StatusInfo, Path: "/home/ada/work/secret-project",
			Detail: "version store for the clone at /home/ada/work/secret-project"},
		{ID: "github-token", Label: "GitHub token", Status: StatusInfo,
			Detail: "remote https://ada:" + leakyValues[1] + "@github.com/acme/x.git and " + leakyValues[6] + " " + leakyValues[4] + " " + leakyValues[5] + " " + leakyValues[8]},
		{ID: "path", Label: "PATH", Status: StatusInfo,
			Detail: `3 entries, searched in order: C:\Users\Ada Lovelace\bin, /Users/ada/go/bin, /usr/bin`},
	}}
}

func TestPublicSummaryLeaksNothing(t *testing.T) {
	summary := hostile().PublicSummary("/home/ada")
	for _, bad := range append([]string{"/home/ada", "/Users/ada", "ada.lovelace", "Lovelace", "secret-project", "acme/", "toml: line", "hunter2"}, leakyValues...) {
		if strings.Contains(summary, bad) {
			t.Errorf("summary contains %q:\n%s", bad, summary)
		}
	}
	for _, want := range []string{"- git: problem, ~/bin/git", "claude: warning, version 2.1.7", "the configuration does not load", "~/go/bin", "omitted"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
}

func TestScrubKeepsOrdinaryText(t *testing.T) {
	in := "git 2.43.0 is recent enough; /usr/local/bin/codex; searched in order: /usr/bin, /bin"
	if got := Scrub(in, "/home/ada"); got != in {
		t.Errorf("Scrub changed %q to %q", in, got)
	}
}

func TestScrubSecretShapesInContext(t *testing.T) {
	for _, in := range []string{
		"password=hunter2", `"api_key": "abc"`, "Authorization: Basic dXNlcjpwYXNz", "https://user:pw@example.com/x",
		"token: abc123",
	} {
		got := Scrub(in, "")
		for _, bad := range []string{"hunter2", `"abc"`, "dXNlcjpwYXNz", "pw@", "abc123"} {
			if strings.Contains(got, bad) {
				t.Errorf("Scrub(%q) = %q still has %q", in, got, bad)
			}
		}
	}
}

func TestIssueURLOpensTheBugFormWithVersionAndOS(t *testing.T) {
	u := IssueURL(hostile(), IssueInfo{Version: "1.2.3", OS: "linux/amd64", Home: "/home/ada"})
	if !strings.HasPrefix(u, IssuesNewURL+"?") {
		t.Fatal(u)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("template") != "bug_report.yml" || q.Get("version") != "1.2.3" || q.Get("os") != "linux/amd64" {
		t.Errorf("query: %v", q)
	}
	if d := q.Get("diagnostics"); !strings.Contains(d, "Overall: problem") || strings.Contains(d, "/home/ada") {
		t.Errorf("diagnostics: %q", d)
	}
}

func TestIssueURLTruncatesGracefully(t *testing.T) {
	r := Report{Status: StatusWarning}
	for i := 0; i < 200; i++ {
		r.Checks = append(r.Checks, Check{ID: "c", Label: "check", Status: StatusInfo, Detail: strings.Repeat("some detail ", 25)})
	}
	u := IssueURL(r, IssueInfo{Version: "1", OS: "linux/amd64"})
	if len(u) > maxIssueURL {
		t.Fatalf("link is %d bytes, over %d", len(u), maxIssueURL)
	}
	parsed, _ := url.Parse(u)
	d := parsed.Query().Get("diagnostics")
	if !strings.HasPrefix(d, "Overall: warning") || !strings.HasSuffix(d, truncationNote) {
		t.Errorf("diagnostics: %q", d)
	}
	// Every line before the note is whole.
	for _, line := range strings.Split(d, "\n")[1:] {
		if line != truncationNote && !strings.HasSuffix(line, "some detail") && !strings.HasSuffix(line, "…") {
			t.Errorf("cut line %q", line)
		}
	}
	// A small report is not truncated.
	if small := IssueURL(Report{Status: StatusOK}, IssueInfo{Version: "1"}); strings.Contains(small, "truncated") {
		t.Error("small report truncated")
	}
}
