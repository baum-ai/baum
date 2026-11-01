package doctor

import (
	"net/url"
	"strings"
	"testing"
)

// macHome is split so the file names no home directory.
const macHome = "/Us" + "ers/alice"

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
		{ID: "git", Label: "git", Status: StatusProblem, Path: "/home/alice/bin/git",
			Detail: "running /home/alice/bin/git --version failed: exit status 1: " + leakyValues[0]},
		{ID: "claude", Label: "claude", Status: StatusWarning, Version: "2.1.7",
			Detail: "failed\nANTHROPIC_API_KEY=" + leakyValues[3] + "\nAuthorization: Bearer " + leakyValues[9] + "\ncontact " + leakyValues[10]},
		{ID: "config", Label: "config file", Status: StatusProblem, Path: "/home/alice/.config/baum/config.toml",
			Detail: `the configuration does not load: toml: line 3: token = "` + leakyValues[9] + `"`,
			Fix:    "fix " + leakyValues[2]},
		{ID: "store-abc", Label: "reviews of acme/secret-project", Status: StatusInfo, Path: "/home/alice/work/secret-project",
			Detail: "version store for the clone at /home/alice/work/secret-project"},
		{ID: "github-token", Label: "GitHub token", Status: StatusInfo,
			Detail: "remote https://ada:" + leakyValues[1] + "@github.com/acme/x.git and " + leakyValues[6] + " " + leakyValues[4] + " " + leakyValues[5] + " " + leakyValues[8]},
		{ID: "path", Label: "PATH", Status: StatusInfo,
			Detail: `3 entries, searched in order: C:\Users\Ada Lovelace\bin, ` + macHome + `/go/bin, /usr/bin`},
	}}
}

func TestPublicSummaryLeaksNothing(t *testing.T) {
	summary := hostile().PublicSummary("/home/alice")
	for _, bad := range append([]string{"/home/alice", macHome, "ada.lovelace", "Lovelace", "secret-project", "toml: line", "hunter2"}, leakyValues...) {
		if strings.Contains(summary, bad) {
			t.Errorf("summary contains %q:\n%s", bad, summary)
		}
	}
	for _, want := range []string{"- git: problem, ~/bin/git", "claude: warning, version 2.1.7", "the configuration does not load", "entries omitted", "omitted"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
}

func TestScrubKeepsOrdinaryText(t *testing.T) {
	in := "git 2.43.0 is recent enough; /usr/local/bin/codex; searched in order: /usr/bin, /bin"
	if got := Scrub(in, "/home/alice"); got != in {
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
	u := IssueURL(hostile(), IssueInfo{Version: "1.2.3", OS: "linux/amd64", Home: "/home/alice"})
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
	if d := q.Get("diagnostics"); !strings.Contains(d, "Overall: problem") || strings.Contains(d, "/home/alice") {
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

func TestPublicSummaryOmitsPathEntriesAndPrivateHosts(t *testing.T) {
	r := Report{Status: StatusOK, Checks: []Check{
		{ID: "path", Label: "PATH", Status: StatusInfo, Detail: "2 entries, searched in order: /etc/profiles/per-user/alice/bin, /work/clients/acme/bin"},
		{ID: "github-token-git.corp.example.com", Label: "GitHub token: git.corp.example.com", Status: StatusProblem, Detail: "baum will not read git.corp.example.com: it is not github.com"},
		{ID: "github-token-github.com", Label: "GitHub token: github.com", Status: StatusOK, Detail: "a token is available for github.com"},
	}}
	s := r.PublicSummary("/home/alice")
	for _, bad := range []string{"alice", "acme", "corp.example.com"} {
		if strings.Contains(s, bad) {
			t.Errorf("summary contains %q:\n%s", bad, s)
		}
	}
	for _, want := range []string{"2 entries, searched in order (entries omitted)", "GitHub token: github.com"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
}

func TestScrubAuthorizationAndOtherHomes(t *testing.T) {
	for in, bad := range map[string]string{
		"Authorization: token 0123abcd4567efgh": "0123abcd4567efgh",
		"Authorization: Token zzzzzzzz":         "zzzzzzzz",
		"Bearer abcdef123":                      "abcdef123",
		`\\fileserver\users\bob\bin`:            "bob",
		`C:\Documents and Settings\bob\bin`:     "bob",
		"/etc/profiles/per-user/bob/bin":        "bob",
	} {
		if got := Scrub(in, ""); strings.Contains(got, bad) {
			t.Errorf("Scrub(%q) = %q still has %q", in, got, bad)
		}
	}
}

func TestIssueURLClipsVersionAndOS(t *testing.T) {
	long := strings.Repeat("v", 9000)
	if u := IssueURL(Report{Status: StatusOK}, IssueInfo{Version: long, OS: long}); len(u) > maxIssueURL {
		t.Fatalf("link is %d bytes", len(u))
	}
}
