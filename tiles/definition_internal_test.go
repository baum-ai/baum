package tiles

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/goldentest"
)

// The digest is a type's identity, so its canonical form is pinned: the
// golden holds the canonical text of testdata/definition.json, written with
// its members out of order, whitespace and needless escapes, and its digest.
// The digest was checked independently: Python's json.dumps(sort_keys=True,
// separators=(",", ":"), ensure_ascii=False) of the same value, its 1E3
// written 1000, hashes to the same sha256.
func TestDefinitionDigestGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/definition.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeCanonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	delete(value.(map[string]any), "digest")
	var b bytes.Buffer
	if err := canonical(&b, value); err != nil {
		t.Fatal(err)
	}
	digest, err := DefinitionDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := "sha256:184a06a29aa05ecf702372634428da2bf849ef55708f949a1f547468c9503193"; digest != want {
		t.Errorf("digest = %s, want %s", digest, want)
	}
	goldentest.Text(t, "testdata/definition.canonical", b.String()+"\n"+digest+"\n")
}

func TestCanonical(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"b":1,"a":2,"A":3,"_":4}`, `{"A":3,"_":4,"a":2,"b":1}`},
		// Member names sort by UTF-16 code units: U+FB33 sorts after the
		// surrogate pair of U+1F600, though its UTF-8 encoding sorts before.
		{`{"דּ":1,"😀":2,"é":3}`, `{"é":3,"😀":2,"דּ":1}`},
		{`[1.0, 1e0, -0, 0.1, 1e21, 1e20, 1.5e-7, 0.000001, 123456789012345680000, 1E3, -2.50]`,
			`[1,1,0,0.1,1e+21,100000000000000000000,1.5e-7,0.000001,123456789012345680000,1000,-2.5]`},
		{`"a\"b\\c\/d\b\f\n\r\t\u0001\u001f\u007f"`, `"a\"b\\c/d\b\f\n\r\t\u0001\u001f` + "\u007f" + `"`},
		{`"<script>&amp;</script>    café 😀"`, `"<script>&amp;</script> ` + "   café 😀" + `"`},
		// No Unicode normalisation: a precomposed and a combining é differ.
		{`["é","é"]`, "[\"é\",\"é\"]"},
		{` { "x" : [ true , false , null ] } `, `{"x":[true,false,null]}`},
	} {
		value, err := decodeCanonical([]byte(tc.in))
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		var b bytes.Buffer
		if err := canonical(&b, value); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if b.String() != tc.want {
			t.Errorf("canonical(%s)\n got %s\nwant %s", tc.in, b.String(), tc.want)
		}
	}
}

func TestDefinitionDigestRefuses(t *testing.T) {
	for name, raw := range map[string]string{
		"invalid UTF-8":       "{\"name\":\"\xff\"}",
		"a lone surrogate":    `{"name":"\ud800"}`,
		"a lone low half":     `{"name":"\udc00x"}`,
		"a repeated member":   `{"name":"a","title":"b","name":"c"}`,
		"a repeated escape":   `{"name":"a","name":"c"}`,
		"a number too large":  `{"kit":1e400}`,
		"not an object":       `[1]`,
		"trailing data":       `{} {}`,
		"not JSON":            `{"name":`,
		"larger than allowed": `{"x":"` + strings.Repeat("a", MaxDefinitionBytes) + `"}`,
	} {
		if digest, err := DefinitionDigest(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: digest %s, want an error", name, digest)
		}
	}
}

// Formatting, member order, escapes and the digest member do not change the
// digest; any change of content does.
func TestDefinitionDigestIdentity(t *testing.T) {
	base := `{"name":"a","kit":1,"title":"T","html":"<p>é</p>"}`
	same := []string{
		` { "kit" : 1.0 , "title":"T", "html":"<p>é</p>","name":"a" } `,
		`{"name":"a","kit":1,"title":"T","html":"<p>é</p>","digest":"sha256:` + strings.Repeat("f", 64) + `"}`,
	}
	different := []string{
		`{"name":"a","kit":2,"title":"T","html":"<p>é</p>"}`,
		`{"name":"a","kit":1,"title":"T ","html":"<p>é</p>"}`,
		"{\"name\":\"a\",\"kit\":1,\"title\":\"T\",\"html\":\"<p>é</p>\"}",
	}
	want, err := DefinitionDigest(json.RawMessage(base))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range same {
		if got, _ := DefinitionDigest(json.RawMessage(raw)); got != want {
			t.Errorf("%s: digest %s, want %s", raw, got, want)
		}
	}
	for _, raw := range different {
		if got, _ := DefinitionDigest(json.RawMessage(raw)); got == want {
			t.Errorf("%s has the same digest", raw)
		}
	}
}

func TestKitSupported(t *testing.T) {
	for major, want := range map[int]bool{0: false, 1: true, KitLatest + 1: false, -1: false} {
		if got := KitSupported(major); got != want {
			t.Errorf("KitSupported(%d) = %v", major, got)
		}
	}
}
