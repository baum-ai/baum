// Code checkout. A generation run reads the repository's code, not only the
// diff, so its risk and behavior judgments can account for callers,
// invariants and existing tests the diff does not show; this file exports the
// head tree and compared merge-base paths into one budgeted archive and
// unpacks it for the run.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// unpackPathError classifies a failure to create an entry on the scratch
// filesystem. Only errors an archive itself can cause, a path that clashes
// with an earlier entry, are a malformed archive; host failures such as a full
// disk or a permission error stay distinguishable so they are not retried as
// bad input.
func unpackPathError(err error) error {
	for _, clash := range []error{syscall.ENOTDIR, syscall.EEXIST, syscall.EISDIR, syscall.ENAMETOOLONG} {
		if errors.Is(err, clash) {
			return ErrInvalidArchive
		}
	}
	return fmt.Errorf("unpack checkout archive: %w", err)
}

// PinnedExport is one pinned snapshot's worth of exports: every call through
// it, for as long as the checkout that holds it runs, reads the same
// physical copy of the repository, so a rebuild cannot mix trees. A missing
// commit or path returns ErrNotFound or an error wrapping it.
type PinnedExport interface {
	ExportCommit(ctx context.Context, commitOID string) (io.ReadCloser, error)
	ExportCommitPath(ctx context.Context, commitOID, path string) (io.ReadCloser, error)
}

// Checkout omission reasons. OmittedBinaryReason is the reason diff.go gives
// a binary hunk, so omitting a file git also diffs as binary leaves its hunk
// as it was.
const (
	OmittedSizeReason      = "omitted_size"
	OmittedSubmoduleReason = "omitted_submodule"
	OmittedBinaryReason    = "binary"
	OmittedBudgetReason    = "omitted_budget"
)

// CheckoutConfig bounds one run's checkout.
// MaxTotalBytes/MaxTotalFiles count only non-omitted content: a file
// omitted for exceeding MaxFileBytes, as binary or as submodule content, does
// not count toward either. WriteArchive omits files that would exceed them
// instead of failing; UnpackCheckout rejects an archive that exceeds them.
// ScratchRoot is local to each process and never crosses to a review agent.
//
// A zero MaxTotalBytes or MaxTotalFiles means unlimited in WriteArchive but
// nothing allowed in UnpackCheckout; MaxFileBytes is always enforced, so zero
// omits or rejects every file.
type CheckoutConfig struct {
	ScratchRoot   string `json:"-"`
	MaxFileBytes  int64  `json:"max_file_bytes"`
	MaxTotalBytes int64  `json:"max_total_bytes"`
	MaxTotalFiles int    `json:"max_total_files"`
}

// CheckoutOmission is one file the checkout left out of the tree.
type CheckoutOmission struct {
	Path   string
	Reason string
}

// firstRegularFileEntry positions tr at the first regular-file entry,
// skipping any leading pseudo-entries: git archive of a single path can
// emit a pax_global_header entry before the real file.
func firstRegularFileEntry(tr *tar.Reader) (*tar.Header, error) {
	for {
		header, err := tr.Next()
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg {
			return header, nil
		}
	}
}

// CheckoutBuilder exports one run's isolated, budgeted code checkout.
type CheckoutBuilder struct {
	// Config bounds the checkout; the builder reads it on every export.
	Config CheckoutConfig
}

func NewCheckoutBuilder(config CheckoutConfig) *CheckoutBuilder {
	return &CheckoutBuilder{Config: config}
}

// submodulePaths reads .gitmodules at commitOID and returns every submodule
// path it declares, repository-relative (submodule contents are omitted).
// git archive silently omits gitlink entries from its output with no tar
// record at all, so .gitmodules — the same file git itself
// uses to know which paths are submodules — is the only source for which paths
// those were. A commit with no .gitmodules file has none.
func submodulePaths(ctx context.Context, pinned PinnedExport, commitOID string) ([]string, error) {
	reader, err := pinned.ExportCommitPath(ctx, commitOID, ".gitmodules")
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = reader.Close() }()

	tarReader := tar.NewReader(reader)
	if _, err := firstRegularFileEntry(tarReader); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, fmt.Errorf("reading .gitmodules archive: %w", err)
	}
	content, err := io.ReadAll(io.LimitReader(tarReader, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading .gitmodules content: %w", err)
	}
	return parseGitmodulesPaths(content), nil
}

// parseGitmodulesPaths extracts every "path = ..." value from a .gitmodules
// file's git-config-format text. It is deliberately minimal — .gitmodules
// is always a short, hand-authored file, and every path assignment this
// misses would simply not be detected as a submodule, the same as if
// .gitmodules were absent, not a security boundary this needs to be exact
// about.
func parseGitmodulesPaths(content []byte) []string {
	var paths []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "path" {
			continue
		}
		if cleaned := filepath.ToSlash(filepath.Clean(strings.TrimSpace(value))); cleaned != "" && cleaned != "." {
			paths = append(paths, cleaned)
		}
	}
	sort.Strings(paths)
	return paths
}

// ApplyCheckoutOmissions marks every diff hunk of an omitted file, at either
// of its paths, textless and appends one generator.truncated note per
// omission reason with the number of files it omitted. A hunk whose old and
// new paths were both omitted takes the reason of the later path.
func ApplyCheckoutOmissions(b *Bundle, omissions []CheckoutOmission) {
	counts := map[string]int{}
	byPath := make(map[string]string, len(omissions))
	for _, omission := range omissions {
		byPath[omission.Path] = omission.Reason
		counts[omission.Reason]++
	}
	for i := range b.Diff {
		hunk := &b.Diff[i]
		reason := ""
		for _, path := range []string{min(hunk.Path, hunk.OldPath), max(hunk.Path, hunk.OldPath)} {
			if path != "" && byPath[path] != "" {
				reason = byPath[path]
			}
		}
		if reason != "" {
			hunk.Patch = ""
			hunk.Textless = true
			hunk.TextlessReason = reason
		}
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		b.Truncated = append(b.Truncated, fmt.Sprintf("checkout omitted %d file(s): %s", counts[reason], reason))
	}
}

// binarySniffBytes is how much of a file git reads to call it binary: a NUL
// byte in the first 8000 bytes (buffer_is_binary in git's xdiff-interface.c).
const binarySniffBytes = 8000

// WriteArchive streams both trees from one pinned snapshot without checking
// them out; an empty mergeBase, which takes no hunks, writes the head tree
// alone. The compared paths go first, at the head and then at the merge base,
// and the rest of the head tree fills what the budget leaves: a file that
// does not fit is omitted with OmittedBudgetReason instead of failing the
// export, so the archive always stays within MaxTotalBytes and MaxTotalFiles.
// Binary files are omitted with OmittedBinaryReason.
//
// A compared path the diff holds as text is never treated as binary: the
// archive applies working-tree-encoding, so a UTF-16 text file arrives with
// NUL bytes although git diffs its canonical content as text.
func (b *CheckoutBuilder) WriteArchive(ctx context.Context, pinned PinnedExport, head, mergeBase string, hunks []Hunk, output io.Writer) (omissions []CheckoutOmission, err error) {
	submodules, err := submodulePaths(ctx, pinned, head)
	if err != nil {
		return nil, err
	}
	diffBinary, diffText := map[string]bool{}, map[string]bool{}
	for _, hunk := range hunks {
		kind := diffText
		if hunk.Textless && hunk.TextlessReason == OmittedBinaryReason {
			kind = diffBinary
		}
		kind[hunk.Path] = true
		if hunk.OldPath != "" {
			kind[hunk.OldPath] = true
		}
	}
	omitted := map[string]string{}
	for _, path := range submodules {
		omitted[path] = OmittedSubmoduleReason
	}
	// A path keeps the first reason either tree omitted it for.
	omit := func(path, reason string) {
		if omitted[path] == "" {
			omitted[path] = reason
		}
	}
	writer := tar.NewWriter(output)
	defer func() { err = errors.Join(err, writer.Close()) }()
	var totalBytes int64
	var totalFiles int
	// Explicit roots keep the two trees separate even when one is empty. A
	// checkout without a merge base, such as a default branch head's, has
	// only head/.
	roots := []string{"head/", "merge-base/"}
	if mergeBase == "" {
		roots = roots[:1]
	}
	for _, prefix := range roots {
		if err := writer.WriteHeader(&tar.Header{Name: prefix, Typeflag: tar.TypeDir, Mode: 0o500}); err != nil {
			return nil, err
		}
	}
	// handled holds every prefixed path already written or omitted, so the
	// full head pass skips the compared paths copied before it.
	handled := map[string]bool{}
	sniff := make([]byte, binarySniffBytes)
	// copyArchive copies regular files under prefix. A non-empty only keeps
	// just that file: a path that is a directory in that tree (a directory
	// the pull request replaced by a file, or the reverse) is skipped, since
	// its files are diff-index paths of their own.
	copyArchive := func(archive io.ReadCloser, prefix, only string) error {
		defer func() { _ = archive.Close() }()
		reader := tar.NewReader(archive)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			header, err := reader.Next()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			path := filepath.ToSlash(filepath.Clean(header.Name))
			if header.Typeflag == tar.TypeReg && (path == "." || !filepath.IsLocal(path) || strings.Contains(path, "\\")) {
				return fmt.Errorf("invalid checkout archive path %q", header.Name)
			}
			if only != "" && path != only {
				if strings.HasPrefix(path, only+"/") {
					return nil
				}
				continue
			}
			if header.Typeflag != tar.TypeReg {
				if only != "" {
					return nil
				}
				continue
			}
			if omitted[path] == OmittedSubmoduleReason || handled[prefix+path] {
				continue
			}
			handled[prefix+path] = true
			if header.Size > b.Config.MaxFileBytes {
				omit(path, OmittedSizeReason)
				continue
			}
			start := sniff[:min(header.Size, binarySniffBytes)]
			if _, err := io.ReadFull(reader, start); err != nil {
				return err
			}
			if (!diffText[path] || diffBinary[path]) && bytes.IndexByte(start, 0) >= 0 {
				omit(path, OmittedBinaryReason)
				continue
			}
			if (b.Config.MaxTotalBytes > 0 && totalBytes+header.Size > b.Config.MaxTotalBytes) ||
				(b.Config.MaxTotalFiles > 0 && totalFiles >= b.Config.MaxTotalFiles) {
				omit(path, OmittedBudgetReason)
				continue
			}
			totalBytes += header.Size
			totalFiles++
			// Do not forward git's PAX metadata or archive timestamps.
			if err := writer.WriteHeader(&tar.Header{Name: prefix + path, Size: header.Size, Mode: 0o400, Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := writer.Write(start); err != nil {
				return err
			}
			if _, err := io.CopyN(writer, reader, header.Size-int64(len(start))); err != nil {
				return err
			}
		}
	}
	copyPath := func(commit, prefix, path string) error {
		archive, err := pinned.ExportCommitPath(ctx, commit, path)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return copyArchive(archive, prefix, path)
	}
	paths := map[string]bool{}
	for _, hunk := range hunks {
		paths[hunk.Path] = true
		if hunk.OldPath != "" {
			paths[hunk.OldPath] = true
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		if omitted[path] != OmittedSubmoduleReason {
			ordered = append(ordered, path)
		}
	}
	sort.Strings(ordered)
	// The compared paths go first, so the budget is spent on them before the
	// rest of the head tree.
	for _, path := range ordered {
		if err := copyPath(head, "head/", path); err != nil {
			return nil, err
		}
	}
	for _, path := range ordered {
		if err := copyPath(mergeBase, "merge-base/", path); err != nil {
			return nil, err
		}
	}
	archive, err := pinned.ExportCommit(ctx, head)
	if err != nil {
		return nil, err
	}
	if err := copyArchive(archive, "head/", ""); err != nil {
		return nil, err
	}
	ordered = ordered[:0]
	for path := range omitted {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		omissions = append(omissions, CheckoutOmission{Path: path, Reason: omitted[path]})
	}
	return omissions, nil
}

// MaxArchiveBytes bounds a checkout archive within the configured caps: its
// content plus tar headers and gzip overhead.
func (c CheckoutConfig) MaxArchiveBytes() int64 {
	return c.MaxTotalBytes + int64(c.MaxTotalFiles)*2048 + (4 << 20)
}

// UnpackCheckout writes a gzip-compressed checkout archive into dir as the
// read-only head/ and, when the archive has one, merge-base/ trees, enforcing
// caps again so a run never reads more than the builder was allowed to
// write. An archive that does not have the shape WriteArchive produces fails
// with ErrInvalidArchive; one over a budget fails with BudgetExceededReason
// naming it, and so does a zero cap.
//
// The caller must bound the archive's size before calling (see
// MaxArchiveBytes): the gzip stream is drained after the tar trailer without
// a limit, and directory entries do not count toward MaxTotalFiles.
func UnpackCheckout(ctx context.Context, archive, dir string, caps CheckoutConfig) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return ErrInvalidArchive
	}
	defer func() { _ = compressed.Close() }()
	reader := tar.NewReader(compressed)
	var bytes int64
	files := 0
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrInvalidArchive
		}
		name := strings.TrimSuffix(entry.Name, "/")
		if !filepath.IsLocal(name) || strings.Contains(name, "\\") || filepath.ToSlash(filepath.Clean(name)) != name {
			return ErrInvalidArchive
		}
		parts := strings.Split(name, "/")
		if parts[0] != "head" && parts[0] != "merge-base" {
			return ErrInvalidArchive
		}
		for _, part := range parts {
			if strings.EqualFold(part, ".git") {
				return ErrInvalidArchive
			}
		}
		path := filepath.Join(dir, name)
		if seen[name] {
			return ErrInvalidArchive
		}
		seen[name] = true
		if entry.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(path, 0700); err != nil {
				return unpackPathError(err)
			}
			continue
		}
		if entry.Typeflag != tar.TypeReg || len(parts) < 2 || entry.Size < 0 {
			return ErrInvalidArchive
		}
		if entry.Size > caps.MaxFileBytes {
			return errors.New(BudgetExceededReason("file_bytes"))
		}
		bytes += entry.Size
		files++
		if bytes > caps.MaxTotalBytes {
			return errors.New(BudgetExceededReason("bytes"))
		}
		if files > caps.MaxTotalFiles {
			return errors.New(BudgetExceededReason("files"))
		}
		// A path that cannot be created inside the tree, such as a file
		// below a path an earlier entry made a file, is a malformed archive.
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return unpackPathError(err)
		}
		output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			return unpackPathError(err)
		}
		_, copyErr := io.CopyN(output, reader, entry.Size)
		err = errors.Join(copyErr, output.Close())
		if err != nil {
			return ErrInvalidArchive
		}
	}
	// Consume the trailer so a corrupt gzip stream cannot pass as a valid archive.
	if _, err := io.Copy(io.Discard, compressed); err != nil {
		return ErrInvalidArchive
	}
	for _, name := range []string{"head", "merge-base"} {
		root := filepath.Join(dir, name)
		if _, err := os.Stat(root); err != nil {
			if name == "merge-base" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return ErrInvalidArchive
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0500)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
