package fix

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/gitlocal"
)

// What a fix's result may not hold, checked after the commit and before the
// push. Only what the fix itself changed is checked (gitlocal.Fix.Changes),
// never what the pull request or a merge of its base already held.
//
//   - A credential: the agent's sandbox confines its writes, not its reads, so
//     a prompt-injected agent can copy the user's keys and tokens into the
//     checkout. A line the fix adds that baum's redaction engine takes for a
//     secret (e-mail addresses aside), or a file at a well-known credential
//     path, is refused with ReasonCredentialAdded. So is a file too large to
//     check. A binary file is not read.
//   - A gitlink: git add records a repository the agent made in the checkout
//     as a commit of another repository, which the push would carry without
//     its content (ReasonNestedRepository).
//   - A workflow or action change, in a fix whose prompt held review
//     comments or a conversation, other people's or an agent's words: a
//     workflow runs with the repository's secrets as soon as it is pushed
//     (ReasonWorkflowChanged). A fix of conflicts or checks alone may change
//     one.

// maxNamed bounds the paths a refusal names.
const maxNamed = 5

// credentialNames are the base names of files that hold a credential
// wherever they are: private SSH keys, git's and curl's stored passwords.
var credentialNames = map[string]bool{
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	"id_ecdsa_sk": true, "id_ed25519_sk": true,
	".git-credentials": true, ".netrc": true, "_netrc": true,
}

// credentialSuffixes are the trailing paths of tools' credential files: the
// AWS CLI's keys and the GitHub CLI's tokens.
var credentialSuffixes = []string{".aws/credentials", "gh/hosts.yml"}

// credentialPath reports a path at a well-known credential file's place.
func credentialPath(p string) bool {
	lower := strings.ToLower(p)
	if credentialNames[path.Base(lower)] {
		return true
	}
	for _, suffix := range credentialSuffixes {
		if lower == suffix || strings.HasSuffix(lower, "/"+suffix) {
			return true
		}
	}
	return false
}

// guard refuses a result that adds a credential or a gitlink, or, when
// untrusted words were part of the prompt, changes a workflow or action.
func guard(ctx context.Context, checkout *gitlocal.Fix, head string, untrusted bool) error {
	changes, err := checkout.Changes(ctx, head)
	if err != nil {
		return fail(ReasonGit, err)
	}
	redactor, err := bundle.NewRedactor()
	if err != nil {
		return fail(ReasonCredentialAdded, errors.New("baum could not check the agent's change for credentials, so nothing was pushed"))
	}
	credentials, unchecked, err := secrets(ctx, redactor, changes)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fail(ReasonCredentialAdded, errors.New("baum could not check the agent's change for credentials, so nothing was pushed"))
	}
	if len(credentials) > 0 {
		return fail(ReasonCredentialAdded, fmt.Errorf("the agent's change adds what looks like a credential in %s, which baum never pushes; check the files in your own checkout if you expected this", named(ctx, redactor, credentials)))
	}
	if len(unchecked) > 0 {
		return fail(ReasonCredentialAdded, fmt.Errorf("the agent's change has files too large to check for credentials (%s), which baum never pushes", named(ctx, redactor, unchecked)))
	}
	var links, flows []string
	for _, change := range changes {
		if change.Mode == gitlocal.ModeGitlink {
			links = append(links, change.Path)
		}
		if untrusted && workflow(change.Path) {
			flows = append(flows, change.Path)
		}
	}
	if len(links) > 0 {
		return fail(ReasonNestedRepository, fmt.Errorf("the agent's change adds or changes a nested repository or submodule (%s), which a fix never pushes", named(ctx, redactor, links)))
	}
	if len(flows) > 0 {
		return fail(ReasonWorkflowChanged, fmt.Errorf("the agent changed GitHub workflow or action files (%s), which a fix of review comments or a conversation never pushes", named(ctx, redactor, flows)))
	}
	return nil
}

// secrets lists the changed paths that hold a credential the change adds
// (credentialPath, or a secret the redactor finds overlapping an added
// line), and those too large to check.
func secrets(ctx context.Context, redactor *bundle.Redactor, changes []gitlocal.Change) (found, unchecked []string, err error) {
	for _, change := range changes {
		if change.Mode == gitlocal.ModeDeleted {
			continue
		}
		if credentialPath(change.Path) {
			found = append(found, change.Path)
			continue
		}
		if change.TooLarge {
			unchecked = append(unchecked, change.Path)
			continue
		}
		if len(change.Added) == 0 {
			continue
		}
		// The whole file is searched, so a key whose first or last line the
		// file already had is still found; only a secret on an added line
		// counts.
		spans, err := redactor.Find(ctx, change.Content)
		if err != nil {
			return nil, nil, err
		}
		for _, span := range spans {
			if !span.Email && overlaps(change.Added, span.Start, span.End) {
				found = append(found, change.Path)
				break
			}
		}
	}
	return found, unchecked, nil
}

// overlaps reports whether [start, end) meets one of ranges.
func overlaps(ranges [][2]int, start, end int) bool {
	for _, r := range ranges {
		if start < r[1] && r[0] < end {
			return true
		}
	}
	return false
}

// workflow reports a GitHub Actions workflow or repository action path.
func workflow(p string) bool {
	lower := strings.ToLower(p)
	return strings.HasPrefix(lower, ".github/workflows/") || strings.HasPrefix(lower, ".github/actions/")
}

// named is the first maxNamed paths, comma-separated, with secrets in them
// masked: the agent chose the names, and a refusal's text is kept with the
// job.
func named(ctx context.Context, redactor *bundle.Redactor, paths []string) string {
	text := strings.Join(paths[:min(len(paths), maxNamed)], ", ")
	if len(paths) > maxNamed {
		text += fmt.Sprintf(" and %d more", len(paths)-maxNamed)
	}
	masked, _, err := redactor.RedactString(ctx, text)
	if err != nil {
		return fmt.Sprintf("%d files", len(paths))
	}
	return masked
}
