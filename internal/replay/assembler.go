package replay

import (
	"context"

	"github.com/baum-ai/baum/internal/assemble"
)

// Assembler is a generate.Assembler that returns a case's saved bundle and
// trees instead of reading GitHub and a clone, so the rest of the pipeline
// runs as it does in the desktop app.
type Assembler struct{ Case Case }

// Assemble implements generate.Assembler. The bundle's bytes are returned
// as they were saved, which is what the model reads and its digest names.
func (a Assembler) Assemble(ctx context.Context, req assemble.Request) (assemble.Result, error) {
	if req.Checkout != nil {
		if err := a.Case.WriteCheckout(ctx, req.Checkout); err != nil {
			return assemble.Result{}, err
		}
	}
	if req.Attachments != nil {
		if err := a.Case.WriteAttachments(req.Attachments); err != nil {
			return assemble.Result{}, err
		}
	}
	b, err := a.Case.FreshBundle()
	if err != nil {
		return assemble.Result{}, err
	}
	pr := b.PullRequest
	return assemble.Result{Bundle: b, JSON: a.Case.BundleJSON, Head: pr.HeadSHA, Base: pr.BaseSHA, MergeBase: pr.MergeBaseSHA}, nil
}
