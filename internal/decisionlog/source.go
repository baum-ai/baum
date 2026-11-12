package decisionlog

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/repos"
)

const sourceContextVersion = "repository-decisions-v1"

// SourceContext records the exact code available to an extraction. The
// transcript remains the evidence for the choice and its stated reason.
type SourceContext struct {
	Version     string                    `json:"version"`
	Repository  string                    `json:"repository"`
	Revision    string                    `json:"revision"`
	Branch      string                    `json:"branch,omitempty"`
	Resolution  string                    `json:"resolution"`
	Omissions   []bundle.CheckoutOmission `json:"omissions"`
	Limitations []string                  `json:"limitations"`
	References  []string                  `json:"references"`
}

type repositorySource struct {
	Directory string
	Context   SourceContext
}

func (s repositorySource) close() { generate.RemoveTree(s.Directory) }

var errSourceUnavailable = errors.New("repository source unavailable")

// prepareRepositorySource exports a pinned commit, never the live checkout.
// The task can read head/ and its manifest but cannot change the user's files.
func prepareRepositorySource(ctx context.Context, dir, repository, revision, branch string, cfg config.Config) (source repositorySource, err error) {
	clone, _, err := openClone(ctx, dir, repository)
	if err != nil {
		return source, fmt.Errorf("%w: %w", errSourceUnavailable, err)
	}
	defer func() { _ = clone.Close() }()
	resolution := "recorded_commit"
	if revision == "" {
		current, err := clone.CurrentBranch(ctx)
		if err != nil || (branch != "" && current != branch) {
			return source, fmt.Errorf("%w: the session branch is not checked out and no commit was recorded", errSourceUnavailable)
		}
		revision, err = clone.HeadCommit(ctx)
		if err != nil || revision == "" {
			return source, fmt.Errorf("%w: the clone has no readable head", errSourceUnavailable)
		}
		resolution = "branch_head"
	} else {
		revision, err = clone.ResolveCommit(ctx, revision)
		if err != nil {
			return source, fmt.Errorf("%w: %w", errSourceUnavailable, err)
		}
	}
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		return source, err
	}
	limits := assemble.LimitsFrom(caps).Checkout
	source.Directory, err = os.MkdirTemp("", agent.RunDirPrefix+"*")
	if err != nil {
		return source, err
	}
	defer func() {
		if err != nil {
			source.close()
		}
	}()
	archive, err := os.CreateTemp("", "baum-source-*.tar.gz")
	if err != nil {
		return source, err
	}
	defer func() { _ = os.Remove(archive.Name()) }()
	compressed := gzip.NewWriter(archive)
	omissions, exportErr := bundle.NewCheckoutBuilder(limits).WriteArchive(ctx, clone, revision, "", nil, compressed)
	err = errors.Join(exportErr, compressed.Close(), archive.Close())
	if err != nil {
		return source, err
	}
	if err = bundle.UnpackHeadCheckout(ctx, archive.Name(), source.Directory, limits); err != nil {
		return source, err
	}
	if omissions == nil {
		omissions = []bundle.CheckoutOmission{}
	}
	source.Context = SourceContext{Version: sourceContextVersion, Repository: repository, Revision: revision, Branch: branch,
		Resolution: resolution, Omissions: omissions, References: []string{}, Limitations: []string{"The source snapshot contains committed files only; uncommitted changes may be described in the session."}}
	if resolution == "branch_head" {
		source.Context.Limitations = append(source.Context.Limitations, "No session commit was recorded; the branch head may have advanced since the session.")
	}
	manifest, err := json.MarshalIndent(source.Context, "", "  ")
	if err != nil {
		return source, err
	}
	err = os.WriteFile(filepath.Join(source.Directory, "source.json"), manifest, 0400)
	return source, err
}

func (s *Store) sourceContext(ctx context.Context, agent, id string) (*SourceContext, error) {
	// A read-only store may predate repository-aware capture. Writable opens
	// install the additive tables before any extraction can be saved.
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='capture_source')`).Scan(&exists); err != nil || !exists {
		return nil, err
	}
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT source_json FROM capture_source WHERE agent=? AND session_id=?`, agent, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var source SourceContext
	if err := json.Unmarshal([]byte(data), &source); err != nil {
		return nil, fmt.Errorf("decoding extraction source: %w", err)
	}
	return &source, nil
}

func sourceCovered(head Session) bool {
	return head.Source != nil && head.Source.Version == sourceContextVersion
}

func (c *Capturer) repositorySource(ctx context.Context, t Target) (repositorySource, error) {
	if c.source != nil {
		return c.source(ctx, t)
	}
	registry, ok := c.Registry.(interface {
		Repositories(context.Context) ([]repos.Repository, error)
	})
	if !ok {
		return repositorySource{}, errSourceUnavailable
	}
	repositories, err := registry.Repositories(ctx)
	if err != nil {
		return repositorySource{}, err
	}
	for _, repository := range repositories {
		if !strings.EqualFold(repository.Repository, t.Repository) {
			continue
		}
		var last error
		for _, clone := range repository.Clones {
			if !clone.Available {
				continue
			}
			source, err := prepareRepositorySource(ctx, clone.Path, t.Repository, t.HeadSHA, t.Branch, c.Config)
			if err == nil {
				return source, nil
			}
			last = err
		}
		return repositorySource{}, errors.Join(errSourceUnavailable, last)
	}
	return repositorySource{}, errSourceUnavailable
}
