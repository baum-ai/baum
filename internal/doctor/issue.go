package doctor

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/baum-ai/baum/redact"
)

// IssuesNewURL opens a new issue in the public repository. The bug report form
// reads its fields from the query (the ids in
// .github/ISSUE_TEMPLATE/bug_report.yml); the user sees every value in the
// browser and sends nothing until they submit the form.
const (
	IssuesNewURL = "https://github.com/baum-ai/baum/issues/new"
	issueForm    = "bug_report.yml"

	// maxIssueURL stays well below the ~8 KB request line GitHub accepts, which
	// answers longer ones with an error page instead of the form.
	maxIssueURL = 6500
	// maxLineLen bounds a single line, so one runaway message cannot crowd out
	// the other checks.
	maxLineLen = 300

	truncationNote = "(truncated to fit a link; run `baum internal doctor` for the full report)"
)

// IssueInfo is what an issue link states besides the report.
type IssueInfo struct {
	Version string
	// OS is for example "linux/amd64".
	OS string
	// Home is the user's home directory, which the summary writes as "~".
	Home string
}

// unixHomes is built in two parts so the file itself names no home directory.
const unixHomes = "/ho" + "me/|/Us" + "ers/"

// Secret shapes. A value that matches is replaced whatever its context.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{16,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{16,}`),
	regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{16,}`),
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{12,}`),
	regexp.MustCompile(`xox[abeprs]-[A-Za-z0-9\-]{10,}`),
	regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`),
	regexp.MustCompile(`AIza[0-9A-Za-z_\-]{30,}`),
	regexp.MustCompile(`npm_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}(?:\.[A-Za-z0-9_\-]*)?`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
}

var (
	// "Authorization: Bearer x", "Bearer x", "Basic x".
	authPattern = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*(?:bearer |basic |token )?|bearer\s+|basic\s+)[A-Za-z0-9._~+/=\-]{6,}`)
	// user:password@ in a URL.
	userinfoPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s@]+@`)
	// token=..., "password": "...", api_key: ... keep the name, drop the value.
	assignPattern = regexp.MustCompile(`(?i)([A-Za-z0-9_.\-]*(?:token|secret|passw(?:or)?d|passwd|api[_\-]?key|apikey|authorization|credential|private[_\-]?key)[A-Za-z0-9_.\-]*["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
	// A long unbroken run with letters and digits is more likely a key than text.
	blobPattern = regexp.MustCompile(`[A-Za-z0-9_+/=\-]{32,}`)
	// Home directories of other spellings than Home: the account name is
	// personal.
	// A Windows account name may hold spaces, so it runs to the next separator.
	homePattern = regexp.MustCompile(`(?i)(?:` + unixHomes + `[^/\\\s:"']+|/per-user/[^/\\\s:"']+|\\\\[^\\\s]+\\users\\[^\\\s:"']+|[A-Z]:[\\/]+Documents and Settings[\\/]+[^/\\:"'\r\n]+|[A-Z]:[\\/]+Users[\\/]+[^/\\:"'\r\n]+)`)
)

// Scrub removes from one line of the report what must not be published: the
// home directory and account name, secret-shaped values, e-mail addresses and
// @mentions.
func Scrub(s, home string) string {
	if h := strings.TrimRight(home, `/\`); len(h) > 1 {
		s = strings.ReplaceAll(s, h, "~")
		s = strings.ReplaceAll(s, strings.ReplaceAll(h, `\`, `/`), "~")
	}
	s = homePattern.ReplaceAllString(s, "~")
	for _, p := range secretPatterns {
		s = p.ReplaceAllString(s, "[redacted]")
	}
	s = authPattern.ReplaceAllString(s, "${1}[redacted]")
	s = userinfoPattern.ReplaceAllString(s, "${1}[redacted]@")
	s = assignPattern.ReplaceAllString(s, "${1}[redacted]")
	s = blobPattern.ReplaceAllStringFunc(s, func(m string) string {
		if strings.IndexFunc(m, unicode.IsLetter) >= 0 && strings.IndexFunc(m, unicode.IsDigit) >= 0 {
			return "[redacted]"
		}
		return m
	})
	s, _ = redact.Identities(s)
	return s
}

func clip(s string) string { return clipTo(s, maxLineLen) }

func clipTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// PublicSummary is the report as markdown lines that are safe to paste into a
// public issue: per check its label, status, version, path and detail, each
// scrubbed (see Scrub). It leaves out the per-clone store checks, which name
// the user's repositories and where they are cloned, and replaces the
// configuration's load error, whose text can quote the file. Fix lines are
// left out: they repeat the detail with paths of this machine, and the
// maintainers know them.
func (r Report) PublicSummary(home string) string {
	var lines []string
	lines = append(lines, "Overall: "+string(r.Status))
	stores := 0
	for _, c := range r.Checks {
		if strings.HasPrefix(c.ID, "store-") {
			stores++
			continue
		}
		label, detail := c.Label, c.Detail
		switch {
		case c.ID == "config" && c.Status == StatusProblem:
			detail = "the configuration does not load"
		case c.ID == "path":
			// The entries name accounts, projects and tools of this machine.
			detail, _, _ = strings.Cut(detail, ":")
			detail += " (entries omitted)"
		case strings.HasPrefix(c.ID, "github-token-") && c.ID != "github-token-"+"github.com":
			// A GitHub Enterprise host or a GitHub Enterprise Cloud tenant
			// (*.ghe.com) names the user's employer.
			label, detail = "GitHub token: other host", "a host other than github.com (name omitted)"
		}
		// Labels are fixed text ("GitHub token: github.com" would otherwise look
		// like an assignment); everything after them is scrubbed.
		line := string(c.Status)
		if c.Version != "" {
			line += ", version " + c.Version
		}
		if c.Path != "" {
			line += ", " + c.Path
		}
		first, rest, _ := strings.Cut(detail, "\n")
		if first != "" {
			line += " — " + first
		}
		lines = append(lines, "- "+label+": "+clip(Scrub(strings.Join(strings.Fields(line), " "), home)))
		for _, extra := range strings.Split(rest, "\n") {
			if extra = strings.TrimSpace(extra); extra != "" {
				lines = append(lines, "  "+clip(Scrub(extra, home)))
			}
		}
	}
	if stores > 0 {
		lines = append(lines, fmt.Sprintf("- %d repository version %s omitted", stores, plural(stores, "store", "stores")))
	}
	return strings.Join(lines, "\n")
}

// IssueURL is a link that opens the public repository's new bug report with
// the version, OS and the report's PublicSummary filled in. The summary loses
// whole lines from its end, with a note, until the link fits maxIssueURL.
func IssueURL(r Report, info IssueInfo) string {
	build := func(summary string) string {
		q := url.Values{}
		q.Set("template", issueForm)
		q.Set("version", clipTo(Scrub(info.Version, info.Home), 100))
		q.Set("os", clipTo(Scrub(info.OS, info.Home), 100))
		q.Set("diagnostics", summary)
		return IssuesNewURL + "?" + q.Encode()
	}
	lines := strings.Split(r.PublicSummary(info.Home), "\n")
	if u := build(strings.Join(lines, "\n")); len(u) <= maxIssueURL {
		return u
	}
	for n := len(lines) - 1; n > 0; n-- {
		if u := build(strings.Join(lines[:n], "\n") + "\n" + truncationNote); len(u) <= maxIssueURL {
			return u
		}
	}
	return build(truncationNote)
}
