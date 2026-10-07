package redact

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	plantedKey = "-----BEGIN RSA " + "PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7Qx1\n-----END RSA PRIVATE KEY-----"
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

// TestEmailRuleMasksWhatThePatternMatched pins shapes where only part of
// the text is an address: the address is masked, the rest is kept.
func TestEmailRuleMasksWhatThePatternMatched(t *testing.T) {
	e := engine(t)
	tests := []struct{ in, want string }{
		{"contact ada@example.com.1 now", "contact xxx@xxxxxxx.xxx.1 now"},
		{"ada@example.com-1", "xxx@xxxxxxx.xxx-1"},
		{"ada@example.com.v2", "xxx@xxxxxxx.xxx.v2"},
		{"ada@example.co.uk2", "xxx@xxxxxxx.xx.uk2"},
		{"ada@-example.com", "xxx@-xxxxxxx.xxx"},
		{"ada@.example.com", "xxx@.xxxxxxx.xxx"},
		{"+ada@example.com.1", "+xxx@xxxxxxx.xxx.1"},
		{"ada@example.com_x", "ada@example.com_x"},
		{"lodash@4.17.21", "lodash@4.17.21"},
	}
	for _, tt := range tests {
		out, _, err := e.Redact(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		if out != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.in, out, tt.want)
		}
	}
}

// TestEmailRuleMasksLongAddressesWhole covers addresses longer than the
// rule's window on either side: Redact still masks every byte of them.
func TestEmailRuleMasksLongAddressesWhole(t *testing.T) {
	e := engine(t)
	tests := []struct{ name, address string }{
		{"local part 200", strings.Repeat("a", 200) + "@example.com"},
		{"local part 300", strings.Repeat("a", 300) + "@example.com"},
		{"domain 300 then tld", "a@" + strings.Repeat("b", 300) + ".com"},
		{"domain of many labels", "a@" + strings.Repeat("abcdefghij.", 30) + "com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := "from " + tt.address + " today"
			out, summary, err := e.Redact(in)
			if err != nil {
				t.Fatal(err)
			}
			masked := strings.Map(func(r rune) rune {
				switch {
				case r >= 'a' && r <= 'z':
					return 'x'
				}
				return r
			}, tt.address)
			if want := "from " + masked + " today"; out != want {
				t.Errorf("Redact = %q, want %q", out, want)
			}
			if summary.ByRule[EmailRuleID] != 1 {
				t.Errorf("summary = %+v, want one e-mail", summary)
			}
		})
	}
}

// cancelAfter is a context that reports cancellation after its Err has
// been asked n times, so a test can cancel a redaction midway.
type cancelAfter struct {
	context.Context
	n int
}

func (c *cancelAfter) Err() error {
	if c.n--; c.n < 0 {
		return context.Canceled
	}
	return nil
}

func TestRedactContextStopsMidway(t *testing.T) {
	e := engine(t)
	text := strings.Repeat("plain text line without secrets\n", 1<<16)
	if _, _, err := e.RedactContext(&cancelAfter{Context: context.Background(), n: 1}, text); !errors.Is(err, context.Canceled) {
		t.Fatalf("RedactContext after cancellation = %v, want context.Canceled", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := e.RedactContext(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RedactContext with a cancelled context = %v", err)
	}
	if out, _, err := e.RedactContext(context.Background(), text); err != nil || out != text {
		t.Fatalf("RedactContext changed clean text or failed: %v", err)
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

func TestIdentitiesRedactsEmailAndDefangsMention(t *testing.T) {
	got, changed := Identities("ask @alice or bob@example.com, not user@host or a/@b")
	if want := "ask alice or [redacted], not user@host or a/@b"; got != want || !changed {
		t.Fatalf("Identities = %q, %v; want %q, true", got, changed, want)
	}
	if got, changed := Identities("nothing to see"); got != "nothing to see" || changed {
		t.Fatalf("Identities changed clean text: %q, %v", got, changed)
	}
}

func TestRedactContextCancelledDuringAddressPass(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("a@b.co\n", 1<<12)
	// The engine pass asks Err a few times; a larger limit cancels in the address pass.
	for n := 1; n < 100000; n *= 2 {
		if _, _, err := e.RedactContext(&cancelAfter{Context: context.Background(), n: n}, text); err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("n=%d: err = %v", n, err)
			}
			return
		}
	}
	t.Fatal("RedactContext never observed cancellation")
}

// FindContext reports one span per finding, covering the bytes
// RedactContext masks, so secrets side by side stay apart.
func TestFindContextReportsEachFinding(t *testing.T) {
	e := engine(t)
	key := "AKIA" + "Z3MQ5TNV2XWB7KDL"
	in := "mail " + email + " keys " + key + "-" + key + " and " + plantedURL
	out, summary, err := e.Redact(in)
	if err != nil {
		t.Fatal(err)
	}
	spans, err := e.FindContext(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	covered := make([]bool, len(in))
	keys := 0
	for _, span := range spans {
		if in[span.Start:span.End] == key {
			keys++
		}
		for i := span.Start; i < span.End; i++ {
			covered[i] = true
		}
	}
	if keys != 2 || len(spans) < summary.Findings {
		t.Fatalf("spans %v for %d findings", spans, summary.Findings)
	}
	for i := range in {
		if out[i] != in[i] && !covered[i] {
			t.Fatalf("byte %d masked outside every span: %v", i, spans)
		}
	}
	if spans, err := e.FindContext(context.Background(), "plain text"); err != nil || len(spans) != 0 {
		t.Fatalf("plain text: %v %v", spans, err)
	}
}

func TestExactWindowWithoutDotIsNotAnAddress(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	text := "a@" + strings.Repeat("b", emailLookahead)
	got, _, err := e.Redact(text)
	if err != nil {
		t.Fatal(err)
	}
	if got != text {
		t.Fatalf("dotless exact-window run was masked: %q", got)
	}
}
