package bundle

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"path"
	"strconv"
	"strings"
)

// PatchFetcher returns the unified diff patch text for one file of a
// comparison, or nil for a binary or unreadable change. An
// ErrOutputTooLarge or ErrPatchUnreadable error makes that file textless.
// paths holds the old and new path of a rename so git can pair them;
// otherwise the one path.
type PatchFetcher func(paths ...string) ([]byte, error)

// BuildDiffIndex splits every compared file into hunks, in Comparison.Files
// order, assigning content-addressed IDs and roles.
//
// Hunk IDs hash the file path and a normalised form of the hunk's changed
// lines (the "+"/"-" lines only, not the "@@" line numbers and not
// unchanged context): this is what makes an ID survive a context shift (the
// same change, with different surrounding context after a nearby edit) and
// a rebase (the same change against a different base). Two hunks in the
// same file whose changed lines are byte-identical (e.g. two blank-line-only
// hunks) hash equally; ordinal suffixes disambiguate them in file order.
func BuildDiffIndex(comparison Comparison, fetch PatchFetcher, roleFor func(path string) string) ([]Hunk, error) {
	hunks, _, err := buildDiffIndex(comparison, fetch, roleFor)
	return hunks, err
}

// DiffLimits bound the diff index assembly reads. A zero field is unlimited.
type DiffLimits struct {
	// MaxFiles bounds the changed files, checked before any patch is read.
	MaxFiles int
	// MaxPatchBytes bounds the patch bytes read across every file.
	MaxPatchBytes int64
	// MaxHunkLines is the per-hunk line cap the host applies later with its
	// note. Parsing keeps one line past it, so that cap and note
	// behave as on the full text while a long hunk is never held whole.
	MaxHunkLines int
}

// buildDiffIndex is BuildDiffIndex that also returns one truncation note
// per kind of patch it could not read.
func buildDiffIndex(comparison Comparison, fetch PatchFetcher, roleFor func(path string) string) ([]Hunk, []string, error) {
	return BuildDiffIndexWithin(comparison, fetch, roleFor, DiffLimits{})
}

// BuildDiffIndexWithin is buildDiffIndex within limits. Exceeding a file
// or byte limit fails with the checkout budget reason naming it.
func BuildDiffIndexWithin(comparison Comparison, fetch PatchFetcher, roleFor func(path string) string, limits DiffLimits) ([]Hunk, []string, error) {
	if limits.MaxFiles > 0 && len(comparison.Files) > limits.MaxFiles {
		return nil, nil, errors.New(BudgetExceededReason("diff files"))
	}
	var hunks []Hunk
	var patchBytes int64
	omitted := map[string]int{}
	keepLines := 0
	if limits.MaxHunkLines > 0 {
		keepLines = limits.MaxHunkLines + 1
	}
	for _, file := range comparison.Files {
		path := effectivePatchPath(file)
		change := diffChangeKind(file)
		patch, err := fetch(ChangePaths(file)...)
		omission := patchOmission(err)
		if err != nil && omission == "" {
			return nil, nil, fmt.Errorf("fetching patch for %s: %w", path, err)
		}
		if omission != "" {
			omitted[omission]++
		}
		patchBytes += int64(len(patch))
		if limits.MaxPatchBytes > 0 && patchBytes > limits.MaxPatchBytes {
			return nil, nil, errors.New(BudgetExceededReason("diff bytes"))
		}
		fileHunks, textless := parseFileHunks(path, patch, keepLines)
		if textless {
			fileHunks = []rawHunk{{id: hunkIDFromDigest(newHunkIDDigest(path))}}
		}
		role := roleFor(path)
		seen := map[string]int{}
		for _, raw := range fileHunks {
			id := raw.id
			seen[id]++
			if n := seen[id]; n > 1 {
				id = fmt.Sprintf("%s-%d", id, n)
			}
			hunk := Hunk{
				ID: id, Path: path, Change: change, Role: role,
				StartLine: raw.newStart, LineCount: raw.newCount,
				Additions: raw.additions, Deletions: raw.deletions,
				DiffURL: diffURL(path, raw.newStart),
			}
			if file.OldPath != "" && file.OldPath != path {
				hunk.OldPath = file.OldPath
			}
			if textless {
				hunk.Textless = true
				hunk.TextlessReason = textlessReason(change, patch)
			} else {
				hunk.Patch = raw.text
			}
			hunks = append(hunks, hunk)
		}
	}
	var notes []string
	if n := omitted[patchTooLarge]; n > 0 {
		notes = append(notes, fmt.Sprintf("patch text omitted for %d file(s) over the patch size limit", n))
	}
	if n := omitted[patchUnreadable]; n > 0 {
		notes = append(notes, fmt.Sprintf("patch text omitted for %d file(s) git could not diff", n))
	}
	return hunks, notes, nil
}

func effectivePatchPath(file FileChange) string {
	if file.NewPath != "" {
		return file.NewPath
	}
	return file.OldPath
}

func diffChangeKind(file FileChange) string {
	switch strings.ToLower(file.Status) {
	case "added", "a":
		return "added"
	case "removed", "deleted", "d":
		return "removed"
	case "renamed", "r":
		return "renamed"
	default:
		return "modified"
	}
}

type rawHunk struct {
	text                 string
	id                   string
	newStart, newCount   int
	additions, deletions int
}

// parseFileHunks splits a unified diff patch into hunks. textless is true
// for a binary change (no "@@" hunk headers found in a non-empty patch, or
// an empty patch for a non-deletion change). keepLines above zero keeps only
// each hunk's leading lines of text; counts and IDs still cover every line.
func parseFileHunks(path string, patch []byte, keepLines int) ([]rawHunk, bool) {
	if len(patch) == 0 {
		return nil, true
	}
	if isBinaryPatch(patch) {
		return nil, true
	}
	scanner := bufio.NewScanner(bytes.NewReader(patch))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var hunks []rawHunk
	var current *rawHunk
	var body strings.Builder
	var id hash.Hash
	lines := 0
	flush := func() {
		if current == nil {
			return
		}
		current.text = strings.TrimRight(body.String(), "\n")
		current.id = hunkIDFromDigest(id)
		hunks = append(hunks, *current)
		current = nil
		body.Reset()
	}
	keep := func(line string) {
		if keepLines > 0 && lines >= keepLines {
			return
		}
		lines++
		body.WriteString(line)
		body.WriteByte('\n')
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "@@ -") {
			flush()
			newStart, newCount := parseHunkHeader(line)
			current = &rawHunk{newStart: newStart, newCount: newCount}
			id = newHunkIDDigest(path)
			lines = 0
			keep(line)
			continue
		}
		if current == nil {
			continue
		}
		keep(line)
		switch {
		case strings.HasPrefix(line, "+"):
			current.additions++
			id.Write([]byte(line))
			id.Write([]byte{'\n'})
		case strings.HasPrefix(line, "-"):
			current.deletions++
			id.Write([]byte(line))
			id.Write([]byte{'\n'})
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		// A line past the scanner's buffer limit (bufio.ErrTooLong, e.g. a
		// vendored minified-JS line beyond git's own 20MiB line limit) stops
		// Scan() early: hunks here would be a silently partial parse with
		// wrong addition/deletion counts and missing content, not a complete
		// patch. Fail closed the same way a binary change does — one
		// textless hunk, not a confidently wrong one.
		return nil, true
	}
	if len(hunks) == 0 {
		return nil, true
	}
	return hunks, false
}

// patchOmission classifies a patch that could not be read: one file over the
// patch size limit, or one git could not diff, is a single textless hunk
// rather than a failure of the whole diff index. Both are unreadable changes,
// so the hunk's reason stays "binary"; the returned kind only feeds the
// truncation notes.
func patchOmission(err error) string {
	switch {
	case errors.Is(err, ErrOutputTooLarge):
		return patchTooLarge
	case errors.Is(err, ErrPatchUnreadable):
		return patchUnreadable
	default:
		return ""
	}
}

const (
	patchTooLarge   = "too_large"
	patchUnreadable = "unreadable"
)

// textlessReason names why a file has no hunks. A mode change, with or
// without a rename, says so rather than passing for a pure rename; an empty
// patch is unreadable and stays "binary".
func textlessReason(change string, patch []byte) string {
	if len(patch) == 0 || isBinaryPatch(patch) {
		return "binary"
	}
	text := "\n" + string(patch)
	if strings.Contains(text, "\nold mode ") || strings.Contains(text, "\nnew mode ") {
		return "mode_change"
	}
	if change == "renamed" {
		return "rename_only"
	}
	return "binary"
}

// isBinaryPatch reports whether git printed a binary marker in the patch
// header: "Binary files a/x and b/y differ", or "GIT binary patch" with
// --binary. Only lines before the first "@@" count, so a text file whose
// added or removed lines quote either phrase stays text.
func isBinaryPatch(patch []byte) bool {
	for _, line := range strings.Split(string(patch), "\n") {
		if strings.HasPrefix(line, "@@") {
			return false
		}
		if strings.HasPrefix(line, "Binary files ") || strings.TrimSuffix(line, "\r") == "GIT binary patch" {
			return true
		}
	}
	return false
}

// parseHunkHeader reads the new-file start line and count from
// "@@ -a,b +c,d @@ ...". A missing ",count" means one line, per unified diff
// convention.
func parseHunkHeader(line string) (start, count int) {
	rest, ok := strings.CutPrefix(line, "@@ -")
	if !ok {
		return 0, 0
	}
	end := strings.Index(rest, " @@")
	if end < 0 {
		return 0, 0
	}
	ranges := strings.Fields(rest[:end])
	if len(ranges) < 2 || !strings.HasPrefix(ranges[1], "+") {
		return 0, 0
	}
	newRange := strings.TrimPrefix(ranges[1], "+")
	parts := strings.SplitN(newRange, ",", 2)
	start, _ = strconv.Atoi(parts[0])
	count = 1
	if len(parts) == 2 {
		count, _ = strconv.Atoi(parts[1])
	}
	return start, count
}

// newHunkIDDigest starts a hunk ID's hash; the hunk's changed lines, each
// with its newline, follow in order.
func newHunkIDDigest(path string) hash.Hash {
	digest := sha256.New()
	digest.Write([]byte(path + "\x00"))
	return digest
}

func hunkIDFromDigest(digest hash.Hash) string {
	return "h_" + hex.EncodeToString(digest.Sum(nil)[:6])
}

func hunkID(path, changedLines string) string {
	digest := newHunkIDDigest(path)
	digest.Write([]byte(changedLines))
	return hunkIDFromDigest(digest)
}

// diffURL follows GitHub's diff fragment shape: diff-<sha256(path)>R<line>.
func diffURL(path string, line int) string {
	digest := sha256.Sum256([]byte(path))
	return fmt.Sprintf("diff-%sR%d", hex.EncodeToString(digest[:]), line)
}

const (
	RoleCode      = "code"
	RoleTest      = "test"
	RoleGenerated = "generated"
	RoleMigration = "migration"
	RoleDocs      = "docs"
	RoleConfig    = "config"
	RoleVendored  = "vendored"
)

// RoleForPath derives a hunk's role from its repository-relative path.
// Rules are checked in order; the first match wins.
func RoleForPath(filePath string) string {
	lower := strings.ToLower(filePath)
	base := path.Base(lower)
	segments := strings.Split(lower, "/")

	for _, segment := range segments {
		if segment == "vendor" || segment == "node_modules" || segment == "third_party" || segment == "vendored" {
			return RoleVendored
		}
	}
	if strings.HasPrefix(lower, "migrations/") || strings.Contains(lower, "/migrations/") {
		return RoleMigration
	}
	if isGeneratedPath(lower, base) {
		return RoleGenerated
	}
	if isTestPath(lower, base) {
		return RoleTest
	}
	if isDocsPath(lower, base) {
		return RoleDocs
	}
	if isConfigPath(lower, base) {
		return RoleConfig
	}
	return RoleCode
}

func isGeneratedPath(lowerPath, base string) bool {
	switch {
	case strings.HasSuffix(lowerPath, ".sql.go"),
		strings.HasSuffix(lowerPath, ".pb.go"),
		strings.HasSuffix(lowerPath, "_generated.go"),
		strings.HasSuffix(lowerPath, ".generated.ts"),
		strings.Contains(lowerPath, "/generated/"),
		isSqlcOutputPath(lowerPath, base):
		return true
	}
	switch base {
	case "go.sum", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "cargo.lock":
		return true
	}
	return false
}

// isSqlcOutputPath reports sqlc's generated Go files inside an sqlc directory.
// Hand-written query and schema sources there (queries/*.sql) stay code, and
// db.go or models.go elsewhere are not sqlc output.
func isSqlcOutputPath(lowerPath, base string) bool {
	if !strings.Contains(lowerPath, "/sqlc/") && !strings.HasPrefix(lowerPath, "sqlc/") {
		return false
	}
	switch base {
	case "querier.go", "db.go", "models.go", "batch.go", "copyfrom.go":
		return true
	}
	return false
}

func isTestPath(lowerPath, base string) bool {
	switch {
	case strings.HasSuffix(lowerPath, "_test.go"),
		strings.HasSuffix(lowerPath, ".test.ts"),
		strings.HasSuffix(lowerPath, ".test.tsx"),
		strings.HasSuffix(lowerPath, ".spec.ts"),
		strings.HasSuffix(lowerPath, ".spec.tsx"):
		return true
	}
	for _, segment := range strings.Split(lowerPath, "/") {
		if segment == "test" || segment == "tests" || segment == "testdata" || segment == "__tests__" || segment == "e2e" {
			return true
		}
	}
	return false
}

func isDocsPath(lowerPath, base string) bool {
	if strings.HasSuffix(lowerPath, ".md") || strings.HasSuffix(lowerPath, ".mdx") || strings.HasSuffix(lowerPath, ".rst") {
		return true
	}
	for _, segment := range strings.Split(lowerPath, "/") {
		if segment == "docs" || segment == "doc" {
			return true
		}
	}
	return false
}

func isConfigPath(lowerPath, base string) bool {
	switch base {
	case "config.toml", ".env", ".env.defaults", "docker-compose.yml", "docker-compose.yaml",
		"justfile", ".gitignore", ".editorconfig", "dockerfile":
		return true
	}
	switch {
	case strings.HasSuffix(lowerPath, ".yaml"), strings.HasSuffix(lowerPath, ".yml"),
		strings.HasSuffix(lowerPath, ".toml"), strings.HasSuffix(lowerPath, ".ini"),
		strings.HasSuffix(lowerPath, ".env"):
		return true
	}
	return false
}
