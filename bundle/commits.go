package bundle

import "context"

// CommitComparer reads one commit's change against its parent.
type CommitComparer interface {
	Compare(ctx context.Context, baseOID, headOID, mode string) (Comparison, error)
}

// AttachCommitChanges reads the line change of every commit made after the
// pull request was opened, for the review exchange's changes after opening.
// A commit the comparer cannot compare keeps no counts, which leaves that
// figure unknown rather than failing the build.
func AttachCommitChanges(ctx context.Context, comparer CommitComparer, pr *PullRequest) error {
	if pr.OpenedAt == nil {
		return nil
	}
	for i := range pr.Commits {
		commit := &pr.Commits[i]
		if commit.CommittedAt == nil || !commit.CommittedAt.After(*pr.OpenedAt) || len(commit.Parents) == 0 {
			continue
		}
		additions, deletions := 0, 0
		if len(commit.Parents) == 1 {
			comparison, err := comparer.Compare(ctx, commit.Parents[0], commit.SHA, "direct")
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			for _, file := range comparison.Files {
				if file.Additions != nil {
					additions += int(*file.Additions)
				}
				if file.Deletions != nil {
					deletions += int(*file.Deletions)
				}
			}
		}
		commit.Additions, commit.Deletions = &additions, &deletions
	}
	return nil
}
