package locallink

import (
	"context"
	"encoding/json"

	"github.com/baum-ai/baum/review"
)

// embedReads bounds the patch reads EmbedPatches has running or answered
// but not yet used, as the viewer's server bounds its patch reads.
const embedReads = 4

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
//
// The patches are read ahead, embedReads at a time, in the order they are
// used, and used one by one in that order, so which hunks get text within
// maxBytes does not depend on which read finishes first. One Patcher reads
// them all, so the clone is opened and the range resolved once.
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
	paths, err := review.HunkPatchPaths(document)
	if err != nil {
		return nil, err
	}
	patcher := new(Patcher)
	defer patcher.Close()
	read := func(ctx context.Context, path string) (PatchResult, error) {
		return patcher.Patch(ctx, PatchRequest{
			Dir: dir, Repository: doc.Review.Repository,
			Head: doc.Review.Head, Base: doc.Review.Base, MergeBase: doc.Review.MergeBase, Path: path,
		})
	}
	type answer struct {
		result PatchResult
		err    error
	}
	ahead, stop := context.WithCancel(ctx)
	defer stop()
	answers := make(map[string]chan answer, len(paths))
	for _, path := range paths {
		answers[path] = make(chan answer, 1)
	}
	// A slot is taken when a read starts and given back when its answer is
	// used, so at most embedReads patches are read or held at once.
	slots := make(chan struct{}, embedReads)
	go func() {
		for _, path := range paths {
			select {
			case slots <- struct{}{}:
			case <-ahead.Done():
				return
			}
			go func() {
				result, err := read(ahead, path)
				answers[path] <- answer{result, err}
			}()
		}
	}()
	return review.EmbedHunkPatches(ctx, document, func(ctx context.Context, path string) (string, bool, error) {
		var a answer
		if ch, listed := answers[path]; listed {
			select {
			case a = <-ch:
				<-slots
			case <-ctx.Done():
				return "", false, ctx.Err()
			}
		} else {
			a.result, a.err = read(ctx, path)
		}
		if a.err != nil {
			return "", false, ctx.Err()
		}
		return a.result.Patch, a.result.Available, nil
	}, maxBytes)
}
