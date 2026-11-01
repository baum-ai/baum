package locallink

import (
	"context"
	"encoding/json"

	"github.com/baum-ai/baum/review"
)

// EmbedPatches is a pull request document that can be read without the
// clone: each indexed hunk without a diff gets its text from its file's
// patch, read from the clone containing dir by the document's own
// repository, head, base and merge base and masked as Patch masks it
// (review.EmbedHunkPatches), and each without a github_url gets the derived
// one. A hunk whose patch the clone cannot provide (missing commits, no
// matching remote, a binary file, a patch over MaxPatchBytes) keeps no diff,
// and so does every hunk once maxBytes of patch text have been read (0 is no
// limit). A failure to read the clone that may pass, such as git failing,
// also leaves the hunk without a diff rather than failing: the document is
// still the one the store holds. Only the context ending fails it.
func EmbedPatches(ctx context.Context, dir string, document []byte, maxBytes int64) ([]byte, error) {
	var doc struct {
		Review struct {
			Repository string `json:"repository"`
			Head       string `json:"head"`
			Base       string `json:"base"`
			MergeBase  string `json:"merge_base"`
		} `json:"review"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, err
	}
	patcher := new(Patcher)
	return review.EmbedHunkPatches(ctx, document, func(ctx context.Context, path string) (string, bool, error) {
		result, err := patcher.Patch(ctx, PatchRequest{
			Dir: dir, Repository: doc.Review.Repository,
			Head: doc.Review.Head, Base: doc.Review.Base, MergeBase: doc.Review.MergeBase, Path: path,
		})
		if err != nil {
			return "", false, ctx.Err()
		}
		return result.Patch, result.Available, nil
	}, maxBytes)
}
