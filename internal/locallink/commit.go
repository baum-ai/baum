package locallink

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baum-ai/baum/internal/gitlocal"
)

// CommitFile is one file a commit changed.
type CommitFile struct {
	// Path is the file's path after the commit (before it, for a deletion).
	Path string `json:"path"`
	// OldPath is the path the file had before a rename.
	OldPath string `json:"old_path,omitempty"`
	// Status is added, modified, deleted or renamed. A change of type (a file
	// to a symlink, say) is modified.
	Status string `json:"status"`
	// Additions and Deletions are the lines the commit added to and removed
	// from the file; both are left out for a binary file.
	Additions *int64 `json:"additions,omitempty"`
	Deletions *int64 `json:"deletions,omitempty"`
	// Submodule and TypeChanged say how to read the file's patch.
	Submodule   bool `json:"-"`
	TypeChanged bool `json:"-"`
}

// CommitDetail is the outcome of ReadCommit.
type CommitDetail struct {
	// Available is true when Body and Files hold the commit's.
	Available bool
	// Code says why they do not, when they do not.
	Code string
	OID  string
	// Parent is the commit's first parent, "" for a root commit.
	Parent string
	// Body is the commit's message after its subject line and the blank lines
	// that follow it, without its final newline.
	Body  string
	Files []CommitFile
}

// Stable reports whether the answer holds while the clone keeps the commit:
// the commit's detail, or one too large to read. The other unavailable
// answers can change when a person fixes the clone.
func (d CommitDetail) Stable() bool { return d.Available || d.Code == CodeTooLarge }

// File is the changed file at path, or the renamed file whose old path is
// path; false when the commit did not change path.
func (d CommitDetail) File(path string) (CommitFile, bool) {
	for _, file := range d.Files {
		if file.Path == path || (file.OldPath != "" && file.OldPath == path) {
			return file, true
		}
	}
	return CommitFile{}, false
}

// CommitRequest names a commit to read and where.
type CommitRequest struct {
	// Dir is a directory inside the clone; "" means the current directory.
	Dir string
	// Repository is the pull request's "owner/name"; the clone must have a
	// remote for it, as Resolve requires.
	Repository string
	// OID is the commit's full id.
	OID string
}

// ReadCommit reads a commit's message body and the files it changed against
// its first parent (the empty tree for a root commit) from the clone
// containing req.Dir, once the clone is matched to req.Repository. It never
// fetches. A commit that cannot be read from this machine is an unavailable
// CommitDetail, not an error.
func ReadCommit(ctx context.Context, req CommitRequest) (CommitDetail, error) {
	unavailable := func(code string) (CommitDetail, error) { return CommitDetail{OID: req.OID, Code: code}, nil }
	repo, code, err := openFor(ctx, req.Dir, req.Repository)
	if err != nil || code != "" {
		return CommitDetail{OID: req.OID, Code: code}, err
	}
	defer func() { _ = repo.Close() }()
	changes, err := repo.CommitChanges(ctx, req.OID)
	if result, handled, err := unreadable(err); handled {
		if err != nil {
			return CommitDetail{}, err
		}
		return unavailable(result.Code)
	}
	detail := CommitDetail{Available: true, OID: changes.OID, Parent: changes.Parent, Body: messageBody(changes.Message), Files: []CommitFile{}}
	for _, change := range changes.Files {
		file := CommitFile{
			Path: change.NewPath, OldPath: change.OldPath, Status: change.Status,
			Additions: change.Additions, Deletions: change.Deletions,
			Submodule:   change.OldMode == "160000" || change.NewMode == "160000",
			TypeChanged: change.Status == "type_changed",
		}
		switch {
		case change.NewPath == "": // a deletion is listed by the path it had
			file.Path, file.OldPath = change.OldPath, ""
		case change.Status != "renamed" || change.OldPath == change.NewPath:
			file.OldPath = ""
		}
		if file.TypeChanged {
			file.Status = "modified"
		}
		detail.Files = append(detail.Files, file)
	}
	return detail, nil
}

// messageBody is a commit message without its subject line: what follows the
// first line, from its first line that is not blank.
func messageBody(message string) string {
	_, body, _ := strings.Cut(message, "\n")
	return strings.TrimRight(strings.TrimLeft(body, "\r\n"), "\n")
}

// CommitPatchRequest names one file of a commit whose patch to read.
type CommitPatchRequest struct {
	Dir, Repository string
	// Commit is the commit as ReadCommit read it, and File one of its files.
	Commit CommitDetail
	File   CommitFile
}

// CommitPatch reads one file's patch in a commit, against the commit's first
// parent (the empty tree for a root commit), as Patch reads a pull request's:
// rename-aware, from its first "@@" line, git's header lines left out. A
// submodule, a binary file or a patch over MaxPatchBytes is an unavailable
// PatchResult.
func CommitPatch(ctx context.Context, req CommitPatchRequest) (PatchResult, error) {
	if !req.Commit.Available || req.File.Path == "" {
		return noPatch(CodeError)
	}
	if req.File.Submodule {
		return noPatch(CodeNoTextPatch)
	}
	repo, code, err := openFor(ctx, req.Dir, req.Repository)
	if err != nil || code != "" {
		return PatchResult{Code: code}, err
	}
	defer func() { _ = repo.Close() }()
	paths := []string{req.File.Path}
	if req.File.OldPath != "" && req.File.OldPath != req.File.Path {
		paths = []string{req.File.OldPath, req.File.Path}
	}
	out, err := repo.CommitFilePatch(ctx, req.Commit.OID, req.Commit.Parent, MaxPatchBytes, paths...)
	if result, handled, err := unreadable(err); handled {
		return result, err
	}
	return patchOf(string(out), req.File.TypeChanged)
}

// openFor opens the clone containing dir for reads, once one of its remotes
// is repository. A clone that cannot answer is a code, not an error.
func openFor(ctx context.Context, dir, repository string) (*gitlocal.Repo, string, error) {
	if repository == "" {
		return nil, CodeNoHead, nil
	}
	if dir == "" {
		dir = "."
	}
	repo, err := gitlocal.Open(ctx, dir, gitlocal.Options{MaxPatchBytes: MaxPatchBytes})
	if err != nil {
		var gitErr *gitlocal.Error
		if errors.As(err, &gitErr) && gitErr.Command == "rev-parse" {
			return nil, CodeNotAClone, nil
		}
		return nil, "", fmt.Errorf("opening the clone: %w", err)
	}
	remotes, err := repo.RemoteURLs(ctx)
	if err != nil {
		_ = repo.Close()
		return nil, "", fmt.Errorf("reading the clone's remotes: %w", err)
	}
	if !anyRemoteIs(remotes, repository) {
		_ = repo.Close()
		return nil, CodeRemoteMismatch, nil
	}
	return repo, "", nil
}
