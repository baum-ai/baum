// Code checkout. A generation run reads the repository's code, not only the
// diff, so its risk and behavior judgments can account for callers,
// invariants and existing tests the diff does not show; this file exports the
// head tree and compared merge-base paths into one budgeted archive and
// unpacks it for the run.
package bundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PinnedExport is one pinned snapshot's worth of exports: every call through
// it, for as long as the checkout that holds it runs, reads the same
// physical copy of the repository, so a rebuild cannot mix trees. A missing
// commit or path returns ErrNotFound or an error wrapping it.
type PinnedExport interface {
	ExportCommit(ctx context.Context, commitOID string) (io.ReadCloser, error)
	ExportCommitPath(ctx context.Context, commitOID, path string) (io.ReadCloser, error)
}

// Checkout omission reasons.
const (
	OmittedSizeReason      = "omitted_size"
	OmittedSubmoduleReason = "omitted_submodule"
)

// CheckoutConfig bounds one run's checkout.
// MaxTotalBytes/MaxTotalFiles count only non-omitted content: a file
// omitted for exceeding MaxFileBytes, or as submodule content, does not
// count toward either.
// ScratchRoot is local to each process and never crosses to a review agent.
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
// path it declares, repository-relative (submodule contents are omitted). git archive silently omits gitlink entries from its
// output with no tar record at all, so .gitmodules — the same file git itself
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

// ApplyCheckoutOmissions marks every diff hunk of an omitted file textless
// and appends one generator.truncated note per omission reason with the
// number of files it omitted.
func ApplyCheckoutOmissions(b *Bundle, omissions []CheckoutOmission) {
	counts := map[string]int{}
	for _, omission := range omissions {
		setOmitted(b, omission.Path, omission.Reason)
		counts[omission.Reason]++
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

// setOmitted marks matching diff hunks textless with the checkout omission reason.
func setOmitted(b *Bundle, path, reason string) {
	for i := range b.Diff {
		if b.Diff[i].Path == path || b.Diff[i].OldPath == path {
			b.Diff[i].Patch = ""
			b.Diff[i].Textless = true
			b.Diff[i].TextlessReason = reason
		}
	}
}

// WriteArchive streams both trees from one pinned snapshot without checking them out.
func (b *CheckoutBuilder) WriteArchive(ctx context.Context, pinned PinnedExport, head, mergeBase string, hunks []Hunk, output io.Writer) (omissions []CheckoutOmission, err error) {
	submodules, err := submodulePaths(ctx, pinned, head)
	if err != nil {
		return nil, err
	}
	omitted := map[string]string{}
	for _, path := range submodules {
		omitted[path] = OmittedSubmoduleReason
	}
	writer := tar.NewWriter(output)
	defer func() { err = errors.Join(err, writer.Close()) }()
	var totalBytes int64
	var totalFiles int
	// Explicit roots keep the two trees separate even when one is empty.
	for _, prefix := range []string{"head/", "merge-base/"} {
		if err := writer.WriteHeader(&tar.Header{Name: prefix, Typeflag: tar.TypeDir, Mode: 0o500}); err != nil {
			return nil, err
		}
	}
	written := map[string]bool{}
	// copyArchive copies regular files under prefix. A non-empty only keeps
	// just that file: a merge-base path that is a directory there (a
	// directory the pull request replaced by a file) is skipped, since its
	// files are diff-index paths of their own.
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
			if omitted[path] == OmittedSubmoduleReason || written[prefix+path] {
				continue
			}
			if header.Size > b.Config.MaxFileBytes {
				omitted[path] = OmittedSizeReason
				continue
			}
			totalBytes += header.Size
			totalFiles++
			if b.Config.MaxTotalBytes > 0 && totalBytes > b.Config.MaxTotalBytes {
				return errors.New(BudgetExceededReason("checkout bytes"))
			}
			if b.Config.MaxTotalFiles > 0 && totalFiles > b.Config.MaxTotalFiles {
				return errors.New(BudgetExceededReason("checkout files"))
			}
			written[prefix+path] = true
			// Do not forward git's PAX metadata or archive timestamps.
			if err := writer.WriteHeader(&tar.Header{Name: prefix + path, Size: header.Size, Mode: 0o400, Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := io.CopyN(writer, reader, header.Size); err != nil {
				return err
			}
		}
	}
	archive, err := pinned.ExportCommit(ctx, head)
	if err != nil {
		return nil, err
	}
	if err := copyArchive(archive, "head/", ""); err != nil {
		return nil, err
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
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		if omitted[path] == OmittedSubmoduleReason {
			continue
		}
		archive, err := pinned.ExportCommitPath(ctx, mergeBase, path)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := copyArchive(archive, "merge-base/", path); err != nil {
			return nil, err
		}
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
// read-only head/ and merge-base/ trees, enforcing caps again so a run never
// reads more than the builder was allowed to write. An archive that does not
// have the shape WriteArchive produces fails with ErrInvalidArchive; one over
// a budget fails with BudgetExceededReason naming it.
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
				return err
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
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			return err
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
	for _, root := range []string{"head", "merge-base"} {
		root := filepath.Join(dir, root)
		if _, err := os.Stat(root); err != nil {
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
