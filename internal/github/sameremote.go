package github

import (
	"strings"

	"github.com/baum-ai/baum/internal/gitlocal"
)

// RemoteNames reports whether the git remote URL names repo, host included: a
// clone of github.com/o/n is not one of ghe.example.com/o/n. The remote is
// read as pull request resolution reads it (ssh.github.com is github.com, an
// SSH alias is resolved through `ssh -G`); a host that is not one gh treats as
// GitHub falls back to the URL's own host, or to an alias without a dot, whose
// owner/name must match.
func RemoteNames(remote string, repo Repository) bool {
	if resolved, err := RepositoryFromRemote(remote); err == nil {
		return strings.EqualFold(resolved.Host, repo.Host) &&
			strings.EqualFold(resolved.Owner, repo.Owner) && strings.EqualFold(resolved.Name, repo.Name)
	}
	host, name, ok := gitlocal.RemoteRepository(remote)
	return ok && name == strings.ToLower(repo.Owner+"/"+repo.Name) &&
		(strings.EqualFold(host, repo.Host) || !strings.Contains(host, "."))
}
