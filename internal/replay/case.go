// Package replay runs the desktop app's review generation again on frozen
// inputs, with no GitHub access, so a prompt or pipeline change can be tried
// repeatedly on real pull requests.
//
// A dataset is a directory holding cases (cases/<id>/), one bare repository
// (checkouts.git) with the trees each case's agent could read, and optionally
// an index (index/cases.jsonl) to select cases by. A replay assembler hands
// the saved bundle and trees to the production pipeline (generate.Run), so
// everything after assembly is what the desktop runs. Nothing is written into
// the dataset.
package replay

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/github"
)

// idPattern is the only shape of a case id: it names a directory and a ref.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}$`)

// Entry is a dataset index line: the facts a selection uses. Fields the index
// does not have decode as their zero value.
type Entry struct {
	ID             string   `json:"id"`
	Split          string   `json:"split"`
	Representative bool     `json:"representative"`
	Primary        bool     `json:"primary"`
	Size           string   `json:"size"`
	SessionVendors []string `json:"session_vendors"`
}

// Dataset is an opened dataset directory.
type Dataset struct {
	Dir string
	// Index is the parsed index/cases.jsonl, in file order; empty when the
	// dataset has none. IndexSHA256 identifies the index file.
	Index       []Entry
	IndexSHA256 string
}

// OpenDataset checks that dir holds cases and the checkouts repository and
// reads its index when there is one.
func OpenDataset(dir string) (*Dataset, error) {
	for _, name := range []string{"cases", "checkouts.git"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("%s is not a replay dataset: it has no %s directory", dir, name)
		}
	}
	d := &Dataset{Dir: dir}
	raw, err := os.ReadFile(filepath.Join(dir, "index", "cases.jsonl"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return d, nil
	case err != nil:
		return nil, err
	}
	sum := sha256.Sum256(raw)
	d.IndexSHA256 = hex.EncodeToString(sum[:])
	for n, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil || entry.ID == "" {
			return nil, fmt.Errorf("index/cases.jsonl line %d is not a case entry", n+1)
		}
		d.Index = append(d.Index, entry)
	}
	return d, nil
}

// IDs lists the case directories, sorted.
func (d *Dataset) IDs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(d.Dir, "cases"))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// Case is one frozen review input.
type Case struct {
	ID string
	// Entry is the case's index line, when the dataset has one.
	Entry *Entry
	// BundleJSON is the exact bytes generation reads. Bundle is their decoding
	// with Digest set as the pipeline sets it: the SHA-256 of BundleJSON
	// without its final newline.
	BundleJSON []byte
	Bundle     bundle.Bundle
	Digest     string
	// FileSHA256 is the SHA-256 of the bundle file, which case.json records.
	FileSHA256 string
	Repository github.Repository
	// HeadTree and MergeBaseTree are the tree ids of the two checkouts.
	HeadTree, MergeBaseTree string

	dir, gitDir string
}

// caseFile is cases/<id>/case.json.
type caseFile struct {
	ID            string `json:"id"`
	BundleSHA256  string `json:"bundle_sha256"`
	HeadTree      string `json:"head_tree"`
	MergeBaseTree string `json:"merge_base_tree"`
}

// LoadCase reads and checks one case: the bundle decodes and matches the
// digest case.json records, both checkout refs resolve to the recorded
// trees, and every image the bundle lists is present and unchanged.
func (d *Dataset) LoadCase(ctx context.Context, id string) (Case, error) {
	fail := func(format string, args ...any) (Case, error) {
		return Case{}, fmt.Errorf("case %s: "+format, append([]any{id}, args...)...)
	}
	if !idPattern.MatchString(id) {
		return fail("not a case id")
	}
	dir := filepath.Join(d.Dir, "cases", id)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fail("there is no such case in the dataset")
	}
	c := Case{ID: id, dir: dir, gitDir: filepath.Join(d.Dir, "checkouts.git")}
	for i := range d.Index {
		if d.Index[i].ID == id {
			c.Entry = &d.Index[i]
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return fail("reading bundle.json: %v", err)
	}
	if err := json.Unmarshal(raw, &c.Bundle); err != nil {
		return fail("bundle.json does not decode as a bundle: %v", err)
	}
	c.BundleJSON = raw
	sum := sha256.Sum256(bytes.TrimSuffix(raw, []byte("\n")))
	c.Digest = hex.EncodeToString(sum[:])
	c.Bundle.Digest = c.Digest
	file := sha256.Sum256(raw)
	c.FileSHA256 = hex.EncodeToString(file[:])
	if c.Repository, err = github.ParseRepository(c.Bundle.Repository); err != nil {
		return fail("the bundle's repository is not owner/name: %v", err)
	}
	if c.Bundle.PullRequest.Number <= 0 || c.Bundle.PullRequest.HeadSHA == "" || c.Bundle.PullRequest.BaseSHA == "" {
		return fail("the bundle has no pull request number, head or base")
	}

	var meta caseFile
	raw, err = os.ReadFile(filepath.Join(dir, "case.json"))
	if err == nil {
		err = json.Unmarshal(raw, &meta)
	}
	if err != nil {
		return fail("reading case.json: %v", err)
	}
	if meta.BundleSHA256 != "" && meta.BundleSHA256 != c.FileSHA256 {
		return fail("bundle.json does not match the digest case.json records")
	}
	trees, err := c.git(ctx, "rev-parse", c.ref("head")+"^{tree}", c.ref("merge-base")+"^{tree}")
	if err != nil {
		return fail("the checkout refs do not resolve: %v", err)
	}
	if fields := strings.Fields(string(trees)); len(fields) == 2 {
		c.HeadTree, c.MergeBaseTree = fields[0], fields[1]
	} else {
		return fail("the checkout refs do not resolve to two trees")
	}
	if meta.HeadTree != "" && meta.HeadTree != c.HeadTree || meta.MergeBaseTree != "" && meta.MergeBaseTree != c.MergeBaseTree {
		return fail("the checkout trees differ from the ones case.json records")
	}
	if err := c.checkAttachments(); err != nil {
		return fail("%v", err)
	}
	return c, nil
}

// FreshBundle decodes BundleJSON again, so a run holds a copy no other run
// shares, with Digest set as LoadCase sets it.
func (c Case) FreshBundle() (bundle.Bundle, error) {
	var b bundle.Bundle
	if err := json.Unmarshal(c.BundleJSON, &b); err != nil {
		return bundle.Bundle{}, err
	}
	b.Digest = c.Digest
	return b, nil
}

// gitEnv is the environment of every git process: the process's own without
// any GIT_ variable, so nothing redirects the repository or its configuration.
func gitEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
}

func (c Case) ref(which string) string { return "refs/cases/" + c.ID + "/" + which }

func (c Case) git(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "safe.directory=*", "--git-dir=" + c.gitDir}, args...)...)
	cmd.Env = gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (c Case) attachmentFile(a bundle.Attachment) string {
	return filepath.Join(c.dir, "attachments", filepath.Base(a.Path))
}

func (c Case) checkAttachments() error {
	for _, a := range c.Bundle.Attachments {
		content, err := os.ReadFile(c.attachmentFile(a))
		if err != nil {
			return fmt.Errorf("the bundle lists image %s but the case has no such file", a.ID)
		}
		if sum := sha256.Sum256(content); hex.EncodeToString(sum[:]) != a.SHA256 {
			return fmt.Errorf("image %s differs from the hash the bundle lists", a.ID)
		}
	}
	return nil
}

// WriteCheckout writes the case's checkout as the archive generation reads
// (bundle.UnpackCheckout): a gzip-compressed tar of the head/ and
// merge-base/ trees, regular files and directories only.
func (c Case) WriteCheckout(ctx context.Context, w io.Writer) error {
	// The archive is read once, on this machine: speed over size.
	zip, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return err
	}
	archive := tar.NewWriter(zip)
	for _, root := range []string{"head", "merge-base"} {
		// The roots always exist, even for a tree with no files.
		if err := archive.WriteHeader(&tar.Header{Name: root + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			return err
		}
		if err := c.copyTree(ctx, archive, root); err != nil {
			return err
		}
	}
	return errors.Join(archive.Close(), zip.Close())
}

// copyTree appends the tree of ref root under the directory root/.
func (c Case) copyTree(ctx context.Context, out *tar.Writer, root string) error {
	cmd := exec.CommandContext(ctx, "git", "-c", "safe.directory=*", "--git-dir="+c.gitDir, "archive", "--format=tar", "--prefix="+root+"/", c.ref(root))
	cmd.Env = gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git archive: %w", err)
	}
	in := tar.NewReader(pipe)
	var copyErr error
	for copyErr == nil {
		var header *tar.Header
		if header, copyErr = in.Next(); copyErr != nil {
			break
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == root || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir) {
			continue
		}
		next := &tar.Header{Name: header.Name, Typeflag: header.Typeflag, Mode: 0o644, Size: header.Size}
		if header.Typeflag == tar.TypeDir {
			next.Mode = 0o755
		}
		if copyErr = out.WriteHeader(next); copyErr == nil && header.Typeflag == tar.TypeReg {
			_, copyErr = io.Copy(out, in)
		}
	}
	if errors.Is(copyErr, io.EOF) {
		copyErr = nil
	}
	_, _ = io.Copy(io.Discard, pipe)
	if waitErr := cmd.Wait(); copyErr == nil && waitErr != nil {
		copyErr = fmt.Errorf("git archive: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return copyErr
}

// WriteAttachments writes the case's images as the plain tar generation reads
// (bundle.UnpackAttachments).
func (c Case) WriteAttachments(w io.Writer) error {
	archive := tar.NewWriter(w)
	for _, a := range c.Bundle.Attachments {
		content, err := os.ReadFile(c.attachmentFile(a))
		if err != nil {
			return err
		}
		if err := archive.WriteHeader(&tar.Header{Name: a.Path, Typeflag: tar.TypeReg, Mode: 0o400, Size: int64(len(content))}); err != nil {
			return err
		}
		if _, err := archive.Write(content); err != nil {
			return err
		}
	}
	return archive.Close()
}
