package redact

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

func TestRedactJSONMasksInsideStringsAndKeepsTheJSONValid(t *testing.T) {
	e := engine(t)
	value := map[string]any{
		"text":   "mail " + email + " and connect to " + plantedURL,
		"quoted": "a \"quoted\" " + email + "\nnext line\té",
		"key":    plantedKey,
		"count":  42,
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	out, summary, err := e.RedactJSON(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Findings == 0 {
		t.Fatal("nothing was found")
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("the redacted text is not JSON: %v\n%s", err, out)
	}
	for _, leaked := range []string{email, "Tr0ub4dor-3xkq", "MIIEpAIBAAKCAQEA7Qx1"} {
		if strings.Contains(string(out), leaked) {
			t.Errorf("%q survived: %s", leaked, out)
		}
	}
	quoted, _ := decoded["quoted"].(string)
	if decoded["count"] != float64(42) || !strings.HasPrefix(quoted, "a \"quoted\" ") ||
		!strings.HasSuffix(quoted, "\nnext line\té") {
		t.Errorf("unmasked content changed: %v", decoded)
	}
}

func TestRedactJSONKeepsTheEscapesASecretSpans(t *testing.T) {
	e := engine(t)
	// The key's newlines are escapes inside one string: the mask must leave
	// each backslash and the letter after it in place.
	data, err := json.Marshal([]string{plantedKey})
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := e.RedactJSON(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []string
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("the redacted text is not JSON: %v\n%s", err, out)
	}
	if strings.Contains(decoded[0], "MIIEpAIBAAKCAQEA7Qx1") || strings.Count(decoded[0], "\n") != 2 {
		t.Errorf("decoded = %q", decoded[0])
	}
}

// A rule anchored at the start of a line does not see one that follows an
// escaped newline or tab in the encoded text; the decoded strings are read
// too, so it masks what Redact masks in the string itself.
func TestRedactJSONMasksWhatFollowsAnEscapedNewline(t *testing.T) {
	e := engine(t)
	env := "PASSWORD=Sup3r$ecret!Pass\nPASSWORD=An0ther$ecret!Pass\nPASSWORD=Th1rd$ecret!Pass\n" +
		"Authorization: Basic YWxhZGRpbjpvcGVuc2VzYW1l\nAuthorization: Basic Zm9vYmFyOmJhenF1dXg9\n"
	cases := map[string][]string{
		env:                            {"Sup3r$ecret", "An0ther$ecret", "Th1rd$ecret", "YWxhZGRpbjpvcGVuc2VzYW1l", "Zm9vYmFyOmJhenF1dXg9"},
		"x\nPASSWORD=Sup3r$ecret!Pass": {"Sup3r$ecret"},
		"x\tAuthorization: Basic YWxhZGRpbjpvcGVuc2VzYW1l": {"YWxhZGRpbjpvcGVuc2VzYW1l"},
		"x\nAuthorization: Basic YWxhZGRpbjpvcGVuc2VzYW1l": {"YWxhZGRpbjpvcGVuc2VzYW1l"},
	}
	for text, secrets := range cases {
		for _, indent := range []bool{false, true} {
			// As a value and as a key.
			var data []byte
			if indent {
				data, _ = json.MarshalIndent(map[string]string{"output_text": text, text: "key"}, "", "  ")
			} else {
				data, _ = json.Marshal(map[string]string{"output_text": text, text: "key"})
			}
			out, _, err := e.RedactJSON(context.Background(), data)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]string
			if err := json.Unmarshal(out, &decoded); err != nil {
				t.Fatalf("%q: not JSON: %v", text, err)
			}
			if len(decoded) != 2 {
				t.Fatalf("%q: keys = %v", text, decoded)
			}
			for _, secret := range secrets {
				if strings.Contains(string(out), secret) {
					t.Errorf("%q (indent %v): %q survived:\n%s", text, indent, secret, out)
				}
			}
			if !strings.Contains(decoded["output_text"], "\n") && strings.Contains(text, "\n") {
				t.Errorf("%q: the newlines were lost: %q", text, decoded["output_text"])
			}
		}
	}
}

func TestRedactJSONLeavesCleanTextAlone(t *testing.T) {
	e := engine(t)
	data := []byte(`{"a":"plain <text> 😀   \"q\" \\ \/","b":[1,true,null,"x\ny"]}`)
	out, _, err := e.RedactJSON(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(data) {
		t.Errorf("out = %s", out)
	}
	for _, bad := range []string{`{"a":"x`, `{"a":"\x"}`, `{"a":"\u12"}`} {
		if _, _, err := e.RedactJSON(context.Background(), []byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestScanStringsDecodesAsEncodingJSONDoes(t *testing.T) {
	for _, value := range []string{"", "a\"b\\c/d", "\b\f\n\r\t", "<>&  ", "é日本😀", "\x01\x1f"} {
		data, _ := json.Marshal(value)
		data = append(data, []byte(` "\ud800x" "\/"`)...)
		literals, err := scanStrings(data)
		if err != nil {
			t.Fatal(err)
		}
		var want string
		_ = json.Unmarshal(data[literals[0].start:literals[0].end], &want)
		var lone string
		_ = json.Unmarshal(data[literals[1].start:literals[1].end], &lone)
		if len(literals) != 3 || literals[0].value != want || literals[1].value != lone || literals[2].value != "/" {
			t.Errorf("%q: decoded %+v", value, literals)
		}
	}
}

var fuzzSecrets = []string{
	"AKIAIOSFODNN7EXAMPLQ",
	"ghp_" + "aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW1xY3z5A",
	"xoxb-" + "123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx",
	email,
	plantedURL,
	plantedKey,
	"glpat-" + "xYz12AbC34dEf56GhI78",
	"PASSWORD=Sup3r$ecret!Pass",
}

var fuzzNoise = []string{`"`, `\`, "\n", "\t", "<", ">", "&", "é", " ", "\x01", " ", "'", "=", ":", "{", "}", "\\n", "\\\"", "日本", "😀", "/"}

// fuzzCore is the part of a planted secret that must not survive.
func fuzzCore(s string) string {
	switch {
	case strings.HasPrefix(s, "-----BEGIN"):
		return "MIIEpAIBAAKCAQEA7Qx1"
	case strings.HasPrefix(s, "postgres"):
		return "Tr0ub4dor-3xkq"
	case strings.Contains(s, "@"):
		return "lovelace@example"
	case strings.HasPrefix(s, "PASSWORD"):
		return "Sup3r$ecret"
	}
	return s[len(s)-10:]
}

func fuzzText(r *rand.Rand, plant bool) (string, []string) {
	var b strings.Builder
	var planted []string
	for i, n := 0, r.Intn(12); i < n; i++ {
		b.WriteString(fuzzNoise[r.Intn(len(fuzzNoise))])
		if plant && r.Intn(4) == 0 {
			s := fuzzSecrets[r.Intn(len(fuzzSecrets))]
			b.WriteString(" " + s + " ")
			planted = append(planted, s)
		} else if r.Intn(2) == 0 {
			b.WriteString("word")
		}
	}
	return b.String(), planted
}

func eachString(v any, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case []any:
		for _, e := range x {
			eachString(e, f)
		}
	case map[string]any:
		for k, e := range x {
			f(k)
			eachString(e, f)
		}
	}
}

// Random objects of awkward strings with secrets planted in values and keys:
// the result is always JSON of the same shape, and nothing Redact masks in a
// string on its own survives.
func TestRedactJSONFuzz(t *testing.T) {
	e := engine(t)
	r := rand.New(rand.NewSource(940))
	ctx := context.Background()
	for iter := 0; iter < 1500; iter++ {
		obj := map[string]any{}
		for k := 0; k < 1+r.Intn(4); k++ {
			key, _ := fuzzText(r, r.Intn(5) == 0)
			val, _ := fuzzText(r, true)
			if r.Intn(3) == 0 {
				obj[key] = []any{val, float64(r.Intn(100)), map[string]any{"inner": val}}
			} else {
				obj[key] = val
			}
		}
		// What Redact masks in each string alone.
		var strict []string
		eachString(obj, func(s string) {
			masked, _, _ := e.Redact(s)
			for _, secret := range fuzzSecrets {
				if strings.Contains(s, fuzzCore(secret)) && !strings.Contains(masked, fuzzCore(secret)) {
					strict = append(strict, fuzzCore(secret))
				}
			}
		})
		for _, indent := range []bool{false, true} {
			var data []byte
			if indent {
				data, _ = json.MarshalIndent(obj, "", "  ")
			} else {
				data, _ = json.Marshal(obj)
			}
			out, _, err := e.RedactJSON(ctx, data)
			if err != nil {
				t.Fatal(err)
			}
			var decoded any
			if err := json.Unmarshal(out, &decoded); err != nil {
				t.Fatalf("iteration %d: not JSON: %v\n%s", iter, err, out)
			}
			if m, ok := decoded.(map[string]any); !ok || len(m) != len(obj) {
				t.Fatalf("iteration %d: the shape changed:\n%s\n%s", iter, data, out)
			}
			var all strings.Builder
			eachString(decoded, func(s string) { all.WriteString(s + "\x00") })
			for _, core := range strict {
				if strings.Contains(all.String(), core) {
					t.Errorf("iteration %d: %q survived:\n%s", iter, core, out)
				}
			}
		}
	}
}
