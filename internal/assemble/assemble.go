// Package assemble builds the baum.review/1 generation bundle for one pull
// request on this machine, with no model call. It runs the same steps in the
// same order as the hosted assembler, over the same bundle package, with
// local sources: the user's clone through gitlocal instead of the hosted
// mirrors, GitHub through the user's own token instead of synchronized rows,
// and the coding-agent sessions in the home directory instead of uploaded
// traces.
//
// The sources are small interfaces, so a test builds a complete bundle
// without a network. Assemble also writes the checkout archive and the
// description image archive that "baum generate" hands to its agent; "baum
// bundle" discards them. The checkout export matters to the bundle itself,
// since a file the checkout budget omits makes its hunks textless.
package assemble

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/lastpersonlabs/baum/bundle"
	"github.com/lastpersonlabs/baum/internal/github"
	"github.com/lastpersonlabs/baum/internal/gitlocal"
)

// GitHub reads one pull request's metadata, checks and conversation;
// *github.Client implements it.
type GitHub interface {
	Fetch(ctx context.Context, repo github.Repository, number int, opts github.FetchOptions) (github.Evidence, error)
}

// Git reads the pull request's commits out of the local clone; *gitlocal.Repo
// implements it. Every method reads objects only: none changes the clone's
// refs, index or working tree.
type Git interface {
	// ResolveCommit expands a commit id the clone holds; a commit it lacks
	// is gitlocal.ErrObjectNotFound. It never fetches.
	ResolveCommit(ctx context.Context, id string) (string, error)
	// RemoteURLs lists the clone's configured remote URLs, from its
	// configuration only.
	RemoteURLs(ctx context.Context) ([]gitlocal.Remote, error)
	// Commits lists the commits reachable from head and not from base,
	// oldest first.
	Commits(ctx context.Context, base, head string) ([]gitlocal.Commit, error)
	// Compare diffs the merge base of base and head against head.
	Compare(ctx context.Context, base, head string) (bundle.Comparison, error)
	// TextPatch diffs paths between exactly the two commits given.
	TextPatch(ctx context.Context, baseOID, headOID string, paths ...string) ([]byte, error)
	// Blob reads a file at a commit; a missing one is bundle.ErrNotFound.
	Blob(ctx context.Context, commitOID, path string) ([]byte, error)
	bundle.PinnedExport
}

var _ Git = (*gitlocal.Repo)(nil)

// Sessions finds the coding-agent sessions linked to the pull request's
// commits, as the bundle's sessions, unredacted: the assembler's redaction
// masks them with the rest of the bundle.
type Sessions interface {
	Evidence(ctx context.Context, commits []gitlocal.Commit, maxPrompts, maxCommandEvents int) ([]bundle.Session, error)
}

// Assembler holds the sources of one assembly. A nil Sessions finds no
// sessions and a nil Images downloads no description image.
type Assembler struct {
	GitHub   GitHub
	Git      Git
	Sessions Sessions
	Images   ImageFetcher
	// Redactor masks the bundle's text; nil builds the default one.
	Redactor *bundle.Redactor
	// Warn receives one line for each input that was left out and why; nil
	// discards them. Bundle JSON never goes here.
	Warn io.Writer
}

// Request names the pull request and the limits its bundle is built within.
type Request struct {
	Repository github.Repository
	Number     int
	Limits     Limits
	// Previous is the pull request's current document, in the form the
	// hosted builder carries it into the next version's bundle; nil when
	// there is none, as for "baum bundle".
	Previous *bundle.PreviousVersion
	// Checkout, when not nil, receives the checkout archive "baum generate"
	// reads: a gzip-compressed tar holding the head/ and merge-base/ trees,
	// as bundle.UnpackCheckout unpacks it. Nil exports the same trees to
	// nowhere, since what the budget omits still shapes the bundle.
	Checkout io.Writer
	// Attachments, when not nil, receives the description images as a plain
	// tar of attachments/<id>.<ext>, as bundle.UnpackAttachments unpacks it.
	Attachments io.Writer
}

// Result is an assembled bundle.
type Result struct {
	// Bundle is the redacted, trimmed bundle with its Digest and
	// MaskedSpanCount set.
	Bundle bundle.Bundle
	// JSON is the bundle as written: the encoding the digest is the SHA-256
	// of, plus a final newline.
	JSON []byte
	// Head, Base and MergeBase are the commits the bundle describes.
	Head, Base, MergeBase string
	// Images are the description images in the attachments archive.
	Images []bundle.DescriptionImage
}

// MissingCommitError reports a pull request commit that is not in the clone.
// Assembly never fetches it: the user's refs stay as they are.
type MissingCommitError struct {
	// Role is "head" or "base".
	Role   string
	SHA    string
	Number int
	// BaseRef is the base branch's name.
	BaseRef string
	// Remote is the clone's remote whose URL names the pull request's
	// repository; "" when none does, and Repository is then named in the hint.
	Remote     string
	Repository string
}

func (e *MissingCommitError) Error() string {
	remote := e.Remote
	if remote == "" {
		remote = "<remote for " + e.Repository + ">"
	}
	fetch := fmt.Sprintf("git fetch %s pull/%d/head", remote, e.Number)
	if e.Role == "base" {
		fetch = "git fetch " + remote + " " + e.BaseRef
	}
	return fmt.Sprintf("the pull request's %s commit %s is not in this clone; run `%s` (or git fetch) and try again. baum does not fetch or change your refs", e.Role, e.SHA, fetch)
}

func (e *MissingCommitError) Unwrap() error { return gitlocal.ErrObjectNotFound }

// Assemble builds the bundle for req. It follows the hosted assembler:
// description images, pull request header, checks and conversation, the diff
// index, sessions, references, REVIEW.md at the base commit, the checkout,
// then caps, redaction, trimming and the digest.
func (a *Assembler) Assemble(ctx context.Context, req Request) (Result, error) {
	if a.GitHub == nil || a.Git == nil {
		return Result{}, errors.New("assembling bundle: no GitHub or git source")
	}
	redactor := a.Redactor
	if redactor == nil {
		var err error
		if redactor, err = bundle.NewRedactor(); err != nil {
			return Result{}, err
		}
	}
	limits := req.Limits

	evidence, err := a.GitHub.Fetch(ctx, req.Repository, req.Number, github.FetchOptions{})
	if err != nil {
		return Result{}, err
	}
	a.warnUnavailable(evidence)
	pr := evidence.PullRequest
	head, err := a.commit(ctx, "head", pr.HeadSHA, req, pr.BaseRef)
	if err != nil {
		return Result{}, err
	}
	base, err := a.commit(ctx, "base", pr.BaseSHA, req, pr.BaseRef)
	if err != nil {
		return Result{}, err
	}

	// Description images come first, as the hosted assembler captures them
	// before it builds the evidence.
	images, contents, altMasked := a.captureImages(ctx, evidence.Images, redactor, limits)
	attachmentsTar := io.Discard
	if req.Attachments != nil {
		attachmentsTar = req.Attachments
	}
	images, err = bundle.WriteAttachments(ctx, images, attachmentsTar, func(_ context.Context, image bundle.DescriptionImage) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(contents[image.ID])), nil
	}, func(image bundle.DescriptionImage, err error) {
		a.warn("description image %s left out: %v", image.ID, err)
	})
	if err != nil {
		return Result{}, fmt.Errorf("writing description images: %w", err)
	}

	b := bundle.Bundle{Sources: bundle.Sources{}}
	evidence.Apply(&b)

	comparison, err := a.Git.Compare(ctx, base, head)
	if err != nil {
		return Result{}, fmt.Errorf("comparing head against the merge base: %w", err)
	}
	mergeBase := comparison.MergeBaseOID
	b.PullRequest.MergeBaseSHA = mergeBase
	hunks, patchNotes, err := bundle.BuildDiffIndexWithin(comparison, func(paths ...string) ([]byte, error) {
		return a.Git.TextPatch(ctx, mergeBase, head, paths...)
	}, bundle.RoleForPath, limits.Caps.DiffLimits())
	if err != nil {
		return Result{}, fmt.Errorf("building the diff index: %w", err)
	}
	b.Diff = hunks
	b.Sources["diff"] = len(hunks) > 0
	b.Truncated = append(b.Truncated, patchNotes...)

	if err := bundle.AttachCommitChanges(ctx, commitComparer{a.Git}, &b.PullRequest); err != nil {
		return Result{}, err
	}

	// Sessions are linked to the commits the clone holds between the merge
	// base and the head, so they follow the comparison that finds the merge
	// base. A session source that fails leaves the bundle without sessions,
	// recorded in its sources, rather than failing the whole assembly.
	if b.Sessions, err = a.sessions(ctx, mergeBase, head, limits.Caps); err != nil {
		return Result{}, err
	}
	b.Sources["sessions"] = len(b.Sessions) > 0
	// A command target needs positive evidence that it is a repository path:
	// a path the diff changes, a directory above one, or a path the session
	// touched. This keeps it independent of the working tree.
	diffPaths := make([]string, 0, 2*len(hunks))
	for _, hunk := range hunks {
		diffPaths = append(diffPaths, hunk.Path, hunk.OldPath)
	}
	bundle.KeepKnownCommandTargets(b.Sessions, bundle.KnownPaths(diffPaths...))

	b.References = bundle.CollectReferences(b)
	b.Sources["references"] = len(b.References) > 0
	b.Attachments = bundle.DescriptionAttachments(b.PullRequest.Body, images)
	b.Sources["attachments"] = len(b.Attachments) > 0

	// The base commit's REVIEW.md applies, so a pull request cannot rewrite
	// the policy its own review follows.
	reviewConfig, err := bundle.LoadReviewConfig(ctx, a.Git, base)
	if err != nil {
		return Result{}, err
	}
	reviewConfig.ChangedInPullRequest = bundle.ChangesReviewConfig(hunks)
	if bundle.CapReviewConfigContent(reviewConfig) {
		b.Truncated = append(b.Truncated, "REVIEW.md content capped to its leading bytes for the size limit")
	}
	b.ReviewConfig = reviewConfig
	b.Sources["review_md"] = reviewConfig.Present
	// The previous version is the caller's: "baum bundle" has none.
	b.Previous = req.Previous
	b.Sources["previous_version"] = b.Previous != nil

	archive, finish := checkoutWriter(req.Checkout)
	omissions, writeErr := bundle.NewCheckoutBuilder(limits.Checkout).WriteArchive(ctx, a.Git, head, mergeBase, b.Diff, archive)
	if err := errors.Join(writeErr, finish()); err != nil {
		return Result{}, fmt.Errorf("exporting the checkout: %w", err)
	}
	bundle.ApplyCheckoutOmissions(&b, omissions)

	// Redaction runs before trimming, so a cut cannot leave part of a secret
	// unmasked.
	bundle.ApplyCaps(&b, limits.Caps)
	if err := bundle.Redact(ctx, &b, redactor); err != nil {
		return Result{}, err
	}
	// Alt text was masked when the images were captured, so Redact found
	// nothing left in it to count.
	b.MaskedSpanCount += altMasked
	bundle.CapAttachments(&b, limits.Caps)
	if err := bundle.Fit(&b, limits.Caps); err != nil {
		return Result{}, err
	}
	if err := bundle.SetDigest(&b); err != nil {
		return Result{}, err
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		return Result{}, fmt.Errorf("encoding bundle: %w", err)
	}
	return Result{
		Bundle: b, JSON: append(encoded, '\n'),
		Head: head, Base: base, MergeBase: mergeBase, Images: images,
	}, nil
}

// commit checks that the pull request's commit is in the clone.
func (a *Assembler) commit(ctx context.Context, role, sha string, req Request, baseRef string) (string, error) {
	if sha == "" {
		return "", fmt.Errorf("GitHub returned no %s commit for pull request %d", role, req.Number)
	}
	oid, err := a.Git.ResolveCommit(ctx, sha)
	switch {
	case errors.Is(err, gitlocal.ErrObjectNotFound):
		return "", &MissingCommitError{
			Role: role, SHA: sha, Number: req.Number, BaseRef: baseRef,
			Remote: a.remoteFor(ctx, req.Repository), Repository: req.Repository.String(),
		}
	case err != nil:
		return "", fmt.Errorf("looking for the %s commit %s in this clone: %w", role, sha, err)
	}
	return oid, nil
}

// remoteFor names the clone's remote whose URL names repo, "" when none does.
func (a *Assembler) remoteFor(ctx context.Context, repo github.Repository) string {
	remotes, err := a.Git.RemoteURLs(ctx)
	if err != nil {
		return ""
	}
	for _, remote := range remotes {
		// Resolve the remote as pull request resolution does (ssh.github.com
		// is github.com, SSH aliases go through ssh -G), and fall back to
		// matching an alias by owner/name.
		if resolved, err := github.RepositoryFromRemote(remote.URL); err == nil {
			if strings.EqualFold(resolved.Host, repo.Host) && strings.EqualFold(resolved.Owner, repo.Owner) && strings.EqualFold(resolved.Name, repo.Name) {
				return remote.Name
			}
			continue
		}
		if host, name, ok := gitlocal.RemoteRepository(remote.URL); ok && name == strings.ToLower(repo.Owner+"/"+repo.Name) &&
			(host == repo.Host || !strings.Contains(host, ".")) {
			return remote.Name
		}
	}
	return ""
}

// sessions finds the linked sessions. A source that fails leaves the bundle
// without sessions and a warning; cancellation is an error.
func (a *Assembler) sessions(ctx context.Context, mergeBase, head string, caps bundle.Caps) ([]bundle.Session, error) {
	if a.Sessions == nil {
		return nil, nil
	}
	commits, err := a.Git.Commits(ctx, mergeBase, head)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		a.warn("sessions left out: listing the pull request's commits in this clone: %v", err)
		return nil, nil
	}
	found, err := a.Sessions.Evidence(ctx, commits, caps.MaxPromptsPerSession, caps.MaxCommandEventsPerSession)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		a.warn("sessions left out: %v", err)
		return nil, nil
	}
	return found, nil
}

// warnUnavailable reports each GitHub part that could not be read.
func (a *Assembler) warnUnavailable(evidence github.Evidence) {
	parts := make([]string, 0, len(evidence.Unavailable))
	for part := range evidence.Unavailable {
		parts = append(parts, part)
	}
	sort.Strings(parts)
	for _, part := range parts {
		a.warn("GitHub %s left out: %v", part, evidence.Unavailable[part])
	}
}

func (a *Assembler) warn(format string, args ...any) {
	if a.Warn != nil {
		_, _ = fmt.Fprintf(a.Warn, "warning: "+format+"\n", args...)
	}
}

// commitComparer adapts the clone to bundle.CommitComparer. A commit's parent
// is an ancestor of it, so the merge base the clone computes is the parent
// itself, which is the direct comparison the mode asks for.
type commitComparer struct{ git Git }

func (c commitComparer) Compare(ctx context.Context, baseOID, headOID, _ string) (bundle.Comparison, error) {
	return c.git.Compare(ctx, baseOID, headOID)
}

// checkoutWriter is where the checkout tar goes: gzip-compressed into out, or
// plain to nowhere when nobody reads it. finish closes the compression.
func checkoutWriter(out io.Writer) (io.Writer, func() error) {
	if out == nil {
		return io.Discard, func() error { return nil }
	}
	compressed := gzip.NewWriter(out)
	return compressed, compressed.Close
}
