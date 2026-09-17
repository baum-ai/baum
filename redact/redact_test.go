package redact

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

const (
	plantedKey = "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7Qx1\n-----END RSA PRIVATE KEY-----"
	plantedURL = "postgres://app:Tr0ub4dor-3xkq@db.internal:5432/app"
	email      = "ada.lovelace@example.com"
)

func engine(t *testing.T) *Engine {
	t.Helper()
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRedactMasksSecretsAndEmails(t *testing.T) {
	e := engine(t)
	in := "mail " + email + " and use " + plantedURL + " for the tests"
	out, summary, err := e.Redact(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Errorf("length changed: %d -> %d", len(in), len(out))
	}
	if strings.Contains(out, "Tr0ub4dor") || strings.Contains(out, "ada.lovelace") || strings.Contains(out, "example.com") {
		t.Errorf("secret or e-mail survived: %q", out)
	}
	if !strings.HasPrefix(out, "mail ") || !strings.HasSuffix(out, " for the tests") {
		t.Errorf("text around the spans was rewritten: %q", out)
	}
	if summary.Findings < 2 || summary.ByRule[EmailRuleID] < 1 {
		t.Errorf("summary = %+v", summary)
	}
}

func TestRedactKeepsLinesAndCode(t *testing.T) {
	e := engine(t)
	in := strings.Join([]string{
		"@@ -1,3 +1,4 @@",
		"+    @Override",
		"+    dsn = \"" + plantedURL + "\"",
		"+    // contact " + email + ".",
		plantedKey,
		"thanks @octocat",
		"",
	}, "\n")
	out, summary, err := e.Redact(in)
	if err != nil {
		t.Fatal(err)
	}
	inLines, outLines := strings.Split(in, "\n"), strings.Split(out, "\n")
	if len(outLines) != len(inLines) {
		t.Fatalf("line count changed: got %d, want %d\n%s", len(outLines), len(inLines), out)
	}
	for i := range inLines {
		if len(outLines[i]) != len(inLines[i]) {
			t.Errorf("line %d changed length: %q -> %q", i+1, inLines[i], outLines[i])
		}
	}
	if strings.Contains(out, "Tr0ub4dor") || strings.Contains(out, "ada.lovelace") || strings.Contains(out, "MIIEpAIBAAKCAQEA7Qx1") {
		t.Errorf("secret or e-mail survived: %q", out)
	}
	for _, kept := range []string{"@@ -1,3 +1,4 @@", "@Override", "thanks @octocat", "// contact ", "."} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q was rewritten:\n%s", kept, out)
		}
	}
	if summary.Findings < 3 || summary.ByRule[EmailRuleID] != 1 {
		t.Errorf("summary = %+v", summary)
	}
}

func TestRedactReaderMasksEmailsInStreams(t *testing.T) {
	e := engine(t)
	in := "{\"text\":\"" + email + " " + plantedURL + "\"}\n{\"text\":\"second record\"}\n"
	var out bytes.Buffer
	summary, err := e.RedactReader(context.Background(), &out, strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "Tr0ub4dor") || strings.Contains(got, "ada.lovelace") {
		t.Errorf("secret or e-mail survived: %q", got)
	}
	if len(got) != len(in) || strings.Count(got, "\n") != 2 || !strings.HasSuffix(got, "second record\"}\n") {
		t.Errorf("stream structure changed: %q", got)
	}
	if summary.ByRule[EmailRuleID] < 1 {
		t.Errorf("summary = %+v", summary)
	}
}

func TestEmailRule(t *testing.T) {
	e := engine(t)
	tests := []struct {
		name   string
		in     string
		masked bool
	}{
		{"plain", "write to " + email + " today", true},
		{"plus and digits", "a.b+tag1@sub.example.co.uk", true},
		{"sentence end", "ask " + email + ".", true},
		{"angle brackets", "From: Ada <" + email + ">", true},
		{"mention", "thanks @octocat", false},
		{"annotation", "@Override", false},
		{"decorator with dot", "@pytest.fixture", false},
		{"scoped package", "npm i @scope/name", false},
		{"no tld", "user@localhost", false},
		{"numeric tld", "user@host.123", false},
		{"bare at", "a @ b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, summary, err := e.Redact(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got := summary.ByRule[EmailRuleID] > 0; got != tt.masked {
				t.Errorf("Redact(%q) masked e-mail = %v, want %v (out %q)", tt.in, got, tt.masked, out)
			}
			if !tt.masked && out != tt.in {
				t.Errorf("Redact(%q) = %q, want unchanged", tt.in, out)
			}
			if tt.masked && strings.Contains(out, "example") {
				t.Errorf("Redact(%q) = %q, domain survived", tt.in, out)
			}
		})
	}
}

func TestStripMentions(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		count int
	}{
		{"none", "plain text", "plain text", 0},
		{"mention", "thanks @octocat", "thanks octocat", 1},
		{"mention at start", "@octocat please", "octocat please", 1},
		{"address left alone", "x@y", "x@y", 0},
		{"path kept", "pkg/@scope/name", "pkg/@scope/name", 0},
		{"two", "@ada cc @octocat", "ada cc octocat", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, count := StripMentions(tt.in)
			if got != tt.want || count != tt.count {
				t.Errorf("StripMentions(%q) = %q, %d; want %q, %d", tt.in, got, count, tt.want, tt.count)
			}
		})
	}
}

func TestEmptyAndProfile(t *testing.T) {
	e := engine(t)
	out, summary, err := e.Redact("")
	if err != nil || out != "" || summary.Findings != 0 {
		t.Errorf("empty: %q %+v %v", out, summary, err)
	}
	if !strings.HasPrefix(e.Profile(), "goredact balanced") {
		t.Errorf("profile = %q", e.Profile())
	}
}
