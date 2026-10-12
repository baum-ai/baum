package locallink

import (
	"net/url"
	"strings"
)

// NormalizeRepository returns the lower-case "owner/name" a git remote URL
// names, and false for a URL that does not name one. It reads the https, http,
// ssh and git forms, the scp-like "user@host:owner/name" form, a ".git"
// suffix and trailing slashes, and ignores credentials, host and port. A local
// path or file URL names no hosted repository.
func NormalizeRepository(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	var path string
	if scheme, _, found := strings.Cut(remote, "://"); found {
		switch strings.ToLower(scheme) {
		case "https", "http", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return "", false
		}
		parsed, err := url.Parse(remote)
		if err != nil || parsed.Host == "" {
			return "", false
		}
		path = parsed.Path
	} else {
		// scp-like: [user@]host:path. A single letter before the colon is a
		// Windows drive, and a slash before it makes the whole a local path.
		host, rest, found := strings.Cut(remote, ":")
		if !found || len(host) < 2 || strings.ContainsAny(host, `/\`) {
			return "", false
		}
		path = rest
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return strings.ToLower(parts[0] + "/" + parts[1]), true
}

// SameRepository reports whether the remote URL names the repository
// "owner/name", ignoring case.
func SameRepository(remote, repository string) bool {
	got, ok := NormalizeRepository(remote)
	return ok && got == strings.ToLower(strings.TrimSpace(repository))
}
