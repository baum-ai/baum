package pricing

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The published standalone module has no platform sibling. In the monorepo,
// any pricing change must update both copies in the same pull request.
func TestMonorepoPricingCopyStaysSynchronized(t *testing.T) {
	root := filepath.Join("..", "..", "..", "platform", "internal", "analytics", "pricing")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("standalone module: no platform source")
	}
	for _, name := range []string{"cost.go", "list.go", "model.go", "rates.go", "prices.json", "cost_test.go", "list_test.go"} {
		upstream, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		local, e := os.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(upstream, local) {
			t.Errorf("%s differs from platform pricing; synchronize both copies", name)
		}
	}
}
