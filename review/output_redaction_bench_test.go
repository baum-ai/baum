package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// benchmarkDocument builds a review document of many sections, each with
// prose, identifiers, risks and evidence. With keys, a few identifiers hold an
// access key and the prose repeats it respelled, as a failing run would.
func benchmarkDocument(sections int, keys bool) []byte {
	var out []any
	prose := strings.Repeat("The retry policy now backs off exponentially and caps at five attempts; contact ops-team@example.com before changing it. ", 8)
	for i := range sections {
		id := fmt.Sprintf("Auth-Flow-%d", i)
		evidence := fmt.Sprintf("EV-Retry-%d", i)
		summary := prose
		if keys && i%50 == 0 {
			evidence = fmt.Sprintf("EV-%s", accessKey(i))
			summary += " It used the key " + strings.ToLower(accessKey(i)) + " to sign."
		}
		out = append(out, map[string]any{
			"id":        id,
			"title":     fmt.Sprintf("Section %d", i),
			"summary":   summary,
			"attention": map[string]any{"refs": []any{evidence}, "text": prose},
			"evidence":  []any{map[string]any{"id": evidence, "text": prose}},
			"risks":     []any{map[string]any{"category": "Retry.Policy", "id": fmt.Sprintf("Token Reuse %d", i), "text": prose}},
		})
	}
	encoded, _ := json.Marshal(map[string]any{"sections": out})
	return encoded
}

// BenchmarkOutputRedaction runs a host's passes, Identifiers, Prepare and
// Document, on a large document, and reports the bytes the closed-value scans
// joined.
func BenchmarkOutputRedaction(b *testing.B) {
	v, err := sharedValidator()
	if err != nil {
		b.Fatal(err)
	}
	redactor, err := bundlepkg.NewRedactor()
	if err != nil {
		b.Fatal(err)
	}
	overlong := []byte(`{"sections":[{"id":"` + strings.Repeat("a_", 32<<10) + leakedAccessKey + `","title":"T"}]}`)
	words := make([]string, 8<<10)
	for i := range words {
		words[i] = fmt.Sprintf("w%x", i)
	}
	distinct := []byte(`{"sections":[{"id":"` + strings.Join(words, "_") + `","title":"T"}]}`)
	for _, c := range []struct {
		name     string
		document []byte
	}{
		{"clean", benchmarkDocument(400, false)},
		{"keys", benchmarkDocument(400, true)},
		{"overlong", overlong},
		{"overlong-distinct", distinct},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.document)))
			b.ReportAllocs()
			scanned := 0
			for range b.N {
				o, err := NewOutputRedactor(v, redactor, &bundlepkg.Bundle{}, nil)
				if err != nil {
					b.Fatal(err)
				}
				masked, err := o.Identifiers(context.Background(), c.document)
				if err != nil {
					b.Fatal(err)
				}
				prepared, _ := v.Prepare(masked, &bundlepkg.Bundle{}, "")
				if _, err := o.Document(context.Background(), prepared); err != nil {
					b.Fatal(err)
				}
				scanned = o.o.scanned
			}
			b.ReportMetric(float64(scanned), "scanned-bytes")
		})
	}
}
