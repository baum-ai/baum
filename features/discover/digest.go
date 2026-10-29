package discover

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// The history digest is a repository's pull request history as feature
// discovery reads it: JSON Lines, a header then one pull request per line
// with the files it touched, oldest first. The agent searches it as a file,
// so its cap only bounds it: HistoryDigestMaxBytes.
const (
	HistoryDigestMaxBytes  = 4 << 20
	historyDigestPaths     = 5
	historyDigestPathDepth = 3
)

// HistoryPullRequest is a pull request as the history digest lists it, with
// the paths its commits touch.
type HistoryPullRequest struct {
	Number int32
	Opened time.Time
	Merged bool
	Title  string
	Branch string
	Paths  []string
}

// HistoryDigestHeader is the digest's first line.
type HistoryDigestHeader struct {
	Repository   string               `json:"repository"`
	PullRequests int                  `json:"pull_requests"`
	Merged       int                  `json:"merged"`
	Open         int                  `json:"open"`
	Trimmed      HistoryDigestTrimmed `json:"trimmed"`
}

// HistoryDigestTrimmed counts the pull requests each trimming step changed.
type HistoryDigestTrimmed struct {
	Branches     int `json:"branches"`
	Paths        int `json:"paths"`
	PullRequests int `json:"pull_requests"`
}

// HistoryDigestLine is one pull request's line.
type HistoryDigestLine struct {
	N      int32    `json:"n"`
	Opened string   `json:"opened"`
	State  string   `json:"state"`
	Title  string   `json:"title"`
	Branch string   `json:"branch,omitempty"`
	Paths  []string `json:"paths"`
}

// HistoryDigest is the history digest of pulls, in their order, within
// HistoryDigestMaxBytes.
func HistoryDigest(repository string, pulls []HistoryPullRequest) []byte {
	return HistoryDigestWithin(repository, pulls, HistoryDigestMaxBytes)
}

// HistoryDigestWithin is the history digest of pulls within maxBytes: over
// it, it drops every branch, then replaces each pull request's files with the
// directories holding most of them from the oldest on, then drops the oldest
// pull requests, each until it fits.
func HistoryDigestWithin(repository string, pulls []HistoryPullRequest, maxBytes int) []byte {
	header := HistoryDigestHeader{Repository: repository, PullRequests: len(pulls)}
	lines := make([]HistoryDigestLine, len(pulls))
	encoded := make([][]byte, len(pulls))
	size := 0
	encode := func(i int) {
		size -= len(encoded[i])
		encoded[i] = jsonLine(lines[i])
		size += len(encoded[i])
	}
	for i, pull := range pulls {
		paths := pull.Paths
		if paths == nil {
			paths = []string{}
		}
		state := "open"
		if pull.Merged {
			state = "merged"
			header.Merged++
		} else {
			header.Open++
		}
		lines[i] = HistoryDigestLine{
			N: pull.Number, Opened: pull.Opened.UTC().Format(time.DateOnly), State: state,
			Title: pull.Title, Branch: pull.Branch, Paths: paths,
		}
		encode(i)
	}
	fits := func() bool { return len(jsonLine(header))+size <= maxBytes }
	if !fits() {
		for i := range lines {
			if lines[i].Branch != "" {
				lines[i].Branch = ""
				encode(i)
				header.Trimmed.Branches++
			}
		}
	}
	for i := 0; i < len(lines) && !fits(); i++ {
		if prefixes := HistoryPathPrefixes(pulls[i].Paths); !slices.Equal(prefixes, lines[i].Paths) {
			lines[i].Paths = prefixes
			encode(i)
			header.Trimmed.Paths++
		}
	}
	dropped := 0
	for ; dropped < len(lines) && !fits(); dropped++ {
		size -= len(encoded[dropped])
		header.PullRequests--
		if lines[dropped].State == "merged" {
			header.Merged--
		} else {
			header.Open--
		}
		header.Trimmed.PullRequests++
	}
	digest := jsonLine(header)
	for _, line := range encoded[dropped:] {
		digest = append(digest, line...)
	}
	return digest
}

// jsonLine is v as one line of JSON, newline included.
func jsonLine(v any) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		panic(err) // the digest's own types always encode
	}
	return buf.Bytes()
}

// HistoryPathPrefixes are the directories, three levels deep and ending in
// "/", holding the most of paths, most first, at most five; a top-level file
// is its own.
func HistoryPathPrefixes(paths []string) []string {
	counts := map[string]int{}
	for _, path := range paths {
		parts := strings.Split(path, "/")
		prefix := path
		if len(parts) > 1 {
			prefix = strings.Join(parts[:min(len(parts)-1, historyDigestPathDepth)], "/") + "/"
		}
		counts[prefix]++
	}
	prefixes := make([]string, 0, len(counts))
	for prefix := range counts {
		prefixes = append(prefixes, prefix)
	}
	slices.SortFunc(prefixes, func(a, b string) int {
		return cmp.Or(counts[b]-counts[a], strings.Compare(a, b))
	})
	return prefixes[:min(len(prefixes), historyDigestPaths)]
}
