package locallink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/gitlocal"
)

// MaxFileBytes is the most of a file ReadFile returns; a longer file is cut
// there and marked truncated.
const MaxFileBytes = 512 << 10

// Codes a FileResult can carry, besides those the sessions answer shares.
const (
	// CodeNotAFile is a path the head does not hold as a file: a directory,
	// a submodule, or no entry at all.
	CodeNotAFile = "not_a_file"
	// CodeNoText is a file whose content is not text (it holds a NUL byte).
	CodeNoText = "no_text"
)

// FileRequest names one file to read at a pull request's head, and where.
type FileRequest struct {
	// Dir is a directory inside the clone; "" means the current directory.
	Dir string
	// Repository is the pull request's "owner/name"; the clone must have a
	// remote for it, as Resolve requires.
	Repository string
	// Head is the commit to read the file at, full or abbreviated.
	Head string
	// Path is the repository-relative path of the file.
	Path string
}

// FileResult is the outcome of ReadFile.
type FileResult struct {
	// Available is true when Content holds the file's text.
	Available bool
	// Content is the file's text, at most MaxFileBytes, with any invalid
	// UTF-8 dropped.
	Content string
	// Truncated says the file goes on past Content.
	Truncated bool
	// Code says why Content is empty, when it is.
	Code string
}

// Stable reports whether the answer holds while the document stays the same:
// the file's text, or a path that has none at the head. The other
// unavailable answers can change when a person fixes the clone.
func (r FileResult) Stable() bool {
	switch {
	case r.Available:
		return true
	case r.Code == CodeNotAFile, r.Code == CodeNoText, r.Code == CodeTooLarge:
		return true
	}
	return false
}

// ReadFile reads one file at req.Head from the clone containing req.Dir, once
// the clone is matched to req.Repository. It never fetches. A file that
// cannot be read from this machine is an unavailable FileResult, not an
// error: a directory, a submodule or a missing path (CodeNotAFile), a binary
// file (CodeNoText), a file over gitlocal's blob cap (CodeTooLarge), a head
// the clone lacks. The caller decides which paths may be read; ReadFile only
// refuses one git would read outside the tree.
func ReadFile(ctx context.Context, req FileRequest) (FileResult, error) {
	unavailable := func(code string) (FileResult, error) { return FileResult{Code: code}, nil }
	if req.Head == "" {
		return unavailable(CodeNoHead)
	}
	repo, code, err := openFor(ctx, req.Dir, req.Repository)
	if err != nil || code != "" {
		return FileResult{Code: code}, err
	}
	defer func() { _ = repo.Close() }()
	head, err := repo.ResolveCommit(ctx, req.Head)
	switch {
	case err == nil:
	case errors.Is(err, gitlocal.ErrObjectNotFound):
		return unavailable(CodeCommitsMissing)
	case errors.Is(err, gitlocal.ErrAmbiguous):
		return unavailable(CodeAmbiguousCommit)
	case errors.Is(err, gitlocal.ErrInvalidInput):
		return unavailable(CodeNoHead)
	default:
		return FileResult{}, fmt.Errorf("resolving commit %s: %w", req.Head, err)
	}
	blob, err := repo.ReadBlob(ctx, head, req.Path)
	switch {
	case err == nil:
	case errors.Is(err, gitlocal.ErrObjectNotFound):
		return unavailable(CodeNotAFile)
	case errors.Is(err, gitlocal.ErrInvalidInput):
		return unavailable(CodeNotAFile)
	case errors.Is(err, bundle.ErrOutputTooLarge):
		return unavailable(CodeTooLarge)
	case errors.Is(err, gitlocal.ErrPartialClone):
		return unavailable(CodePartialClone)
	default:
		return FileResult{}, fmt.Errorf("reading %s: %w", req.Path, err)
	}
	return fileText(blob.Content), nil
}

// fileText is the answer for a file's bytes: no text when they hold a NUL
// byte, else at most MaxFileBytes of them as UTF-8.
func fileText(content []byte) FileResult {
	if bytes.IndexByte(content, 0) >= 0 {
		return FileResult{Code: CodeNoText}
	}
	truncated := len(content) > MaxFileBytes
	if truncated {
		content = content[:MaxFileBytes]
	}
	// A cut through a character leaves its first bytes, which go with the
	// rest of what is not UTF-8.
	return FileResult{Available: true, Content: strings.ToValidUTF8(string(content), ""), Truncated: truncated}
}

// maxCitedPathBytes bounds a cited path; none a file system allows is longer.
const maxCitedPathBytes = 4096

// CitedFiles reads the paths a review document cites, which are the only
// ones the app's file routes read (GET /api/file, a group's file): the context's references
// (summary.context.<part>.refs[].path), the hunks' paths and old paths
// (index.hunks[].path, old_path), and the path of every anchor of type
// "file" anywhere in the document. A document that does not parse cites none.
func CitedFiles(document []byte) map[string]struct{} {
	paths := map[string]struct{}{}
	var parsed struct {
		Summary struct {
			Context map[string]*struct {
				Refs []struct {
					Path string `json:"path"`
				} `json:"refs"`
			} `json:"context"`
		} `json:"summary"`
		Index struct {
			Hunks []struct {
				Path    string `json:"path"`
				OldPath string `json:"old_path"`
			} `json:"hunks"`
		} `json:"index"`
	}
	if json.Unmarshal(document, &parsed) != nil {
		return paths
	}
	add := func(path string) {
		if path != "" && len(path) <= maxCitedPathBytes {
			paths[path] = struct{}{}
		}
	}
	for _, part := range parsed.Summary.Context {
		if part == nil {
			continue
		}
		for _, ref := range part.Refs {
			add(ref.Path)
		}
	}
	for _, hunk := range parsed.Index.Hunks {
		add(hunk.Path)
		add(hunk.OldPath)
	}
	// Anchors sit in many places (sections, risks, behaviours, cards), so
	// every object of the document is looked at.
	var tree any
	if json.Unmarshal(document, &tree) == nil {
		walkFileAnchors(tree, add)
	}
	return paths
}

func walkFileAnchors(node any, add func(string)) {
	switch value := node.(type) {
	case map[string]any:
		if kind, _ := value["type"].(string); kind == "file" {
			if path, ok := value["path"].(string); ok {
				add(path)
			}
		}
		for _, child := range value {
			walkFileAnchors(child, add)
		}
	case []any:
		for _, child := range value {
			walkFileAnchors(child, add)
		}
	}
}
