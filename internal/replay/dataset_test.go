package replay_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/replay"
)

// BAUM_REPLAY_DATASET names a real dataset directory. The test loads every
// case through the loader and the replay assembler, with no model, and logs
// only counts and case ids.
func TestEveryCaseOfADatasetLoads(t *testing.T) {
	dir := os.Getenv("BAUM_REPLAY_DATASET")
	if dir == "" {
		t.Skip("BAUM_REPLAY_DATASET is not set")
	}
	ds, err := replay.OpenDataset(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := ds.IDs()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		t.Fatal(err)
	}
	limits := assemble.LimitsFrom(caps)
	scratch := t.TempDir()
	var withPrevious, previousAndImages, loaded, lossless, withImages, unpacked, indexed, noMergeBase int
	for i, id := range ids {
		c, err := ds.LoadCase(ctx(), id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		loaded++
		// The bundle type should still hold every field of the capture.
		if again, err := json.Marshal(c.Bundle); err == nil && bytes.Equal(append(again, '\n'), c.BundleJSON) {
			lossless++
		}
		if c.Bundle.Previous != nil {
			withPrevious++
			if len(c.Bundle.Attachments) > 0 {
				previousAndImages++
			}
		}
		if c.Entry != nil {
			indexed++
		}
		result, err := replay.Assembler{Case: c}.Assemble(ctx(), assemble.Request{Repository: c.Repository, Number: c.Bundle.PullRequest.Number, Limits: limits})
		if err != nil || result.Bundle.Digest != c.Digest || string(result.JSON) != string(c.BundleJSON) {
			t.Errorf("%s: assembler: %v", id, err)
			continue
		}
		if c.Bundle.PullRequest.MergeBaseSHA == "" {
			noMergeBase++
		}
		if len(c.Bundle.Attachments) > 0 {
			withImages++
		}
		// The archives are what generation unpacks. A checkout is tens of
		// megabytes, so a sample is written and read through, and a smaller
		// one goes through bundle.UnpackCheckout.
		if i%10 != 0 {
			continue
		}
		if err := checkArchives(t, c, limits, i%50 == 0, scratch); err != nil {
			t.Errorf("%s: archives: %v", id, err)
			continue
		}
		unpacked++
	}
	t.Logf("with a previous version %d, of which also offering images %d", withPrevious, previousAndImages)
	t.Logf("cases %d, loaded %d, archives checked %d, bundle type lossless %d, with images %d, indexed %d, bundles without a merge base %d", len(ids), loaded, unpacked, lossless, withImages, indexed, noMergeBase)
	if len(ds.Index) > 0 && len(ds.Index) != len(ids) {
		t.Logf("the index has %d entries for %d case directories", len(ds.Index), len(ids))
	}
	if len(ds.Index) > 0 {
		reportStructure(t, ds)
	}
}

// reportStructure logs counts only: how many pull requests have a chain of two,
// three, four or more revisions, and how many cases each detector matches, by
// split.
func reportStructure(t *testing.T, ds *replay.Dataset) {
	for _, split := range []string{"calibration", "held_out"} {
		chains, err := ds.SelectChains(replay.Selection{Split: split})
		if err != nil {
			t.Errorf("%s chains: %v", split, err)
			continue
		}
		length := map[int]int{}
		for _, c := range chains {
			length[min(len(c.IDs), 4)]++
		}
		t.Logf("%s: pull requests with chains of 2 revisions %d, 3 revisions %d, 4 or more %d", split, length[2], length[3], length[4])
		for _, kind := range []string{"all cases", "primary cases", "representative cases"} {
			var found []string
			for _, detector := range []string{"schema", "api", "any", "none"} {
				ids, err := ds.Select(replay.Selection{Split: split, Primary: kind == "primary cases", Representative: kind == "representative cases", Detector: detector})
				if err != nil {
					t.Errorf("%s %s: %v", split, detector, err)
				}
				found = append(found, fmt.Sprintf("%s %d", detector, len(ids)))
			}
			t.Logf("%s, %s: detector matches: %s", split, kind, strings.Join(found, ", "))
		}
	}
}

// checkArchives writes the case's archives and checks the checkout's shape:
// regular files and directories under head/ and merge-base/, within the caps.
// With unpack set, both archives are also unpacked as generation does.
func checkArchives(t *testing.T, c replay.Case, limits assemble.Limits, unpack bool, scratch string) error {
	t.Helper()
	var checkout bytes.Buffer
	if err := c.WriteCheckout(ctx(), &checkout); err != nil {
		return err
	}
	zip, err := gzip.NewReader(bytes.NewReader(checkout.Bytes()))
	if err != nil {
		return err
	}
	var files int
	var total int64
	roots := map[string]bool{}
	for r := tar.NewReader(zip); ; {
		header, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		root, _, _ := strings.Cut(name, "/")
		if !filepath.IsLocal(name) || (root != "head" && root != "merge-base") || slices.Contains(strings.Split(name, "/"), ".git") {
			return fmt.Errorf("entry %q is not allowed", name)
		}
		roots[root] = true
		if header.Typeflag == tar.TypeReg {
			files++
			total += header.Size
			if header.Size > limits.Checkout.MaxFileBytes {
				return fmt.Errorf("entry %q is over the file cap", name)
			}
		} else if header.Typeflag != tar.TypeDir {
			return fmt.Errorf("entry %q is not a file or directory", name)
		}
	}
	if len(roots) != 2 || int64(files) > int64(limits.Checkout.MaxTotalFiles) || total > limits.Checkout.MaxTotalBytes {
		return fmt.Errorf("the checkout has %d files, %d bytes and %d roots", files, total, len(roots))
	}
	var images bytes.Buffer
	if err := c.WriteAttachments(&images); err != nil {
		return err
	}
	if !unpack {
		return nil
	}
	run, err := os.MkdirTemp(scratch, "run-")
	if err != nil {
		return err
	}
	defer generate.RemoveTree(run)
	archive := filepath.Join(scratch, "checkout.tar.gz")
	if err := os.WriteFile(archive, checkout.Bytes(), 0o600); err != nil {
		return err
	}
	if err := bundle.UnpackCheckout(ctx(), archive, run, limits.Checkout); err != nil {
		return err
	}
	if len(c.Bundle.Attachments) == 0 {
		return nil
	}
	list := filepath.Join(scratch, "attachments.tar")
	if err := os.WriteFile(list, images.Bytes(), 0o600); err != nil {
		return err
	}
	return bundle.UnpackAttachments(ctx(), list, run, c.Bundle.Attachments, limits.MaxImageBytes)
}
