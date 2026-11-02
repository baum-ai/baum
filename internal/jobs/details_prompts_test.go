package jobs

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
)

// A details retry writes sections with the prompts the version was written
// with: a changed staged prompt stands it down and stores nothing, while
// unchanged prompts, built in or custom, continue.
func TestDetailsRetryRefusesChangedStagedPrompts(t *testing.T) {
	ctx := context.Background()
	local := &Local{}
	details := Prepared{Item: Item{Kind: KindDetails, Version: 1}}

	t.Run("built in to built in", func(t *testing.T) {
		o, versions, key, _ := detailsFixture(t)
		if _, err := local.completeDetails(ctx, versions, nil, key, details, o); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("changed override", func(t *testing.T) {
		o, versions, key, _ := detailsFixture(t)
		saved, _ := versions.Latest(ctx, key)
		o.StagedPrompts = prompt.StagedPrompts{Overrides: prompt.Overrides{prompt.IDReviewStagedShared: customText(t, prompt.IDReviewStagedShared, "CHANGED")}}
		_, err := local.completeDetails(ctx, versions, nil, key, details, o)
		if err == nil || !strings.Contains(err.Error(), "the review prompts changed since this review was written") {
			t.Fatalf("err %v", err)
		}
		latest, _ := versions.Latest(ctx, key)
		if latest.Metadata.Version != saved.Metadata.Version || !bytes.Equal(latest.Document, saved.Document) {
			t.Fatal("a refused retry stored something")
		}
	})
}
