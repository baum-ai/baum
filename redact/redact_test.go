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

func TestRedactStringMasksSecretsAndIdentities(t *testing.T) {
	e := engine(t)
	in := "ping @octocat, mail " + email + " and use " + plantedURL + " for the tests"
	out, summary, err := e.RedactString(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Tr0ub4dor") {
		t.Errorf("password survived: %q", out)
	}
	if strings.Contains(out, email) || strings.Contains(out, "@octocat") {
		t.Errorf("identity survived: %q", out)
	}
	if !strings.Contains(out, "ping octocat,") || !strings.Contains(out, Marker) {
		t.Errorf("unexpected rewrite: %q", out)
	}
	if summary.Secrets < 1 || summary.Identities < 2 || summary.Total() != summary.Secrets+summary.Identities {
		t.Errorf("summary = %+v", summary)
	}
	if len(summary.ByRule) == 0 {
		t.Errorf("ByRule should name the rule: %+v", summary)
	}
}

func TestRedactLinesPreservesLineCountAndMentions(t *testing.T) {
	e := engine(t)
	in := strings.Join([]string{
		"@@ -1,3 +1,4 @@",
		"+    @Override",
		"+    dsn = \"" + plantedURL + "\"",
		"+    // contact " + email,
		plantedKey,
		"",
	}, "\n")
	out, summary, err := e.RedactLines(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Count(out, "\n"), strings.Count(in, "\n"); got != want {
		t.Fatalf("line count changed: got %d, want %d\n%s", got, want, out)
	}
	if strings.Contains(out, "Tr0ub4dor") || strings.Contains(out, email) || strings.Contains(out, "MIIEpAIBAAKCAQEA7Qx1") {
		t.Errorf("secret or e-mail survived: %q", out)
	}
	if !strings.Contains(out, "@Override") || !strings.Contains(out, "@@ -1,3 +1,4 @@") {
		t.Errorf("hunk header or annotation was rewritten: %q", out)
	}
	if summary.Secrets < 2 || summary.Identities < 1 {
		t.Errorf("summary = %+v", summary)
	}
}

func TestRedactReaderStreamsSecretsOnly(t *testing.T) {
	e := engine(t)
	in := "{\"text\":\"" + email + " " + plantedURL + "\"}\n{\"text\":\"second record\"}\n"
	var out bytes.Buffer
	summary, err := e.RedactReader(context.Background(), &out, strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Tr0ub4dor") {
		t.Errorf("password survived: %q", out.String())
	}
	if !strings.Contains(out.String(), email) {
		t.Errorf("reader path must leave identities alone: %q", out.String())
	}
	if strings.Count(out.String(), "\n") != 2 || !strings.HasSuffix(out.String(), "second record\"}\n") {
		t.Errorf("stream structure changed: %q", out.String())
	}
	if summary.Secrets < 1 || summary.Identities != 0 {
		t.Errorf("summary = %+v", summary)
	}
}

func TestRedactIdentities(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		count int
	}{
		{"none", "plain text", "plain text", 0},
		{"email", "mail " + email + " now", "mail " + Marker + " now", 1},
		{"mention", "thanks @octocat", "thanks octocat", 1},
		{"mention at start", "@octocat please", "octocat please", 1},
		{"decorator kept mid-token", "use x@y", "use x@y", 0},
		{"path kept", "pkg/@scope/name", "pkg/@scope/name", 0},
		{"both", email + " cc @octocat", Marker + " cc octocat", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, count := RedactIdentities(tt.in)
			if got != tt.want || count != tt.count {
				t.Errorf("RedactIdentities(%q) = %q, %d; want %q, %d", tt.in, got, count, tt.want, tt.count)
			}
		})
	}
}

func TestEmptyInput(t *testing.T) {
	e := engine(t)
	for name, fn := range map[string]func(string) (string, Summary, error){"string": e.RedactString, "lines": e.RedactLines} {
		out, summary, err := fn("")
		if err != nil || out != "" || summary.Total() != 0 {
			t.Errorf("%s: %q %+v %v", name, out, summary, err)
		}
	}
	if !strings.HasPrefix(engine(t).Profile(), "goredact balanced") {
		t.Errorf("profile = %q", engine(t).Profile())
	}
}
