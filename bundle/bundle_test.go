package bundle

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The host-only sort keys and commit parents are exported for the host that
// sets them but must never reach the encoded bundle a model reads.
func TestEncodedBundleOmitsHostOnlyFields(t *testing.T) {
	b := Bundle{
		PullRequest: PullRequest{Commits: []Commit{{SHA: "abc", Parents: []string{"p0", "p1"}}}},
		Sessions: []Session{{
			RootSessionID: "s1", Confidence: 0.9, HasStartedAt: true, StartedAtValue: time.Unix(100, 0),
		}},
		MaskedSpanCount: 3,
		Digest:          "d",
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"Parents", "parents", "Confidence", "confidence", "HasStartedAt", "StartedAtValue", "MaskedSpanCount", "Digest"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("encoded bundle contains %q: %s", leaked, encoded)
		}
	}
}
