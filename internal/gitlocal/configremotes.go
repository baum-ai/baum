package gitlocal

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// configRemotes caches the remote URLs read from a git config file by the
// file's modification time and size, so listing repositories on every poll
// costs one stat per clone.
var configRemotes struct {
	sync.Mutex
	files map[string]configRemotesEntry
}

type configRemotesEntry struct {
	modTime time.Time
	size    int64
	urls    []string
}

// ConfigRemoteURLs lists the remote URLs written in the "config" file of a
// clone's common git directory, exactly as written. It reads that one file
// directly, without running git, so it follows no "include" and no
// "insteadOf" rewrite, and returns nothing when the file cannot be read.
func ConfigRemoteURLs(commonDir string) []string {
	path := filepath.Join(commonDir, "config")
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil
	}
	configRemotes.Lock()
	defer configRemotes.Unlock()
	if e, ok := configRemotes.files[path]; ok && e.modTime.Equal(info.ModTime()) && e.size == info.Size() {
		return slices.Clone(e.urls)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	urls := parseConfigRemoteURLs(string(data))
	if configRemotes.files == nil {
		configRemotes.files = map[string]configRemotesEntry{}
	}
	configRemotes.files[path] = configRemotesEntry{modTime: info.ModTime(), size: info.Size(), urls: urls}
	return slices.Clone(urls)
}

// RemoteEarlierNames lists the recorded earlier names (see AddRenamed), as
// lower-case "owner/name"s, that remote URLs of the clone with the common git
// directory still use, sorted. Unlike RemoteRepository it does not map them to
// the current name.
func RemoteEarlierNames(commonDir string) []string {
	var names []string
	for _, u := range ConfigRemoteURLs(commonDir) {
		host, repository, ok := rawRemoteRepository(u)
		if ok && renamable(host) && RenamedTo(repository) != repository && !slices.Contains(names, repository) {
			names = append(names, repository)
		}
	}
	slices.Sort(names)
	return names
}

// parseConfigRemoteURLs reads the "url" values of the [remote "name"] sections
// of a git config file.
func parseConfigRemoteURLs(text string) []string {
	var urls []string
	inRemote := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section, rest, ok := strings.Cut(strings.TrimPrefix(line, "["), "]")
			kind, _, _ := strings.Cut(strings.TrimSpace(section), " ")
			inRemote = strings.EqualFold(kind, "remote") && strings.Contains(section, `"`)
			if !ok {
				continue
			}
			// A key may follow the header on the same line.
			line = strings.TrimSpace(rest)
		}
		if !inRemote {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "url") {
			continue
		}
		if value = configValue(value); value != "" {
			urls = append(urls, value)
		}
	}
	return urls
}

// configValue unquotes a git config value and drops a trailing comment.
func configValue(v string) string {
	var out strings.Builder
	quoted := false
	v = strings.TrimSpace(v)
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\' && i+1 < len(v):
			i++
			out.WriteByte(v[i])
		case c == '"':
			quoted = !quoted
		case !quoted && (c == '#' || c == ';'):
			return strings.TrimSpace(out.String())
		default:
			out.WriteByte(c)
		}
	}
	return strings.TrimSpace(out.String())
}
