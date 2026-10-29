package fix

import (
	"regexp"
	"strings"
)

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?(-->|$)`)
	mdImage     = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLink      = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	htmlTag     = regexp.MustCompile(`</?[A-Za-z][^>]*>`)
	boldLine    = regexp.MustCompile(`^(\*\*|__)\s*(.+?)\s*(\*\*|__)[.:]?$`)
	lineMarker  = regexp.MustCompile(`^(#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+)+`)
	openMark    = regexp.MustCompile("(^|[\\s(])[*_`~]+(\\S)")
	closeMark   = regexp.MustCompile("(\\S)[*_`~]+($|[\\s).,:;!?])")
)

// Headline is a review comment's point in one line of plain text: the first
// line written wholly in bold among its first few, where review bots put
// their title, or else its first line of text. HTML comments, tags, images
// (such as priority badges), code blocks and Markdown markup are left out,
// and a link keeps its text.
func Headline(body string) string {
	body = htmlComment.ReplaceAllString(body, "")
	body = mdImage.ReplaceAllString(body, "")
	body = htmlTag.ReplaceAllString(body, "")
	var lines []string
	fenced := false
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || line == "" {
			continue
		}
		lines = append(lines, lineMarker.ReplaceAllString(line, ""))
	}
	for _, line := range lines[:min(len(lines), 4)] {
		if m := boldLine.FindStringSubmatch(line); m != nil {
			return inline(m[2])
		}
	}
	for _, line := range lines {
		if text := inline(line); text != "" {
			return text
		}
	}
	return ""
}

// inline drops a line's emphasis and code marks and keeps a link's text.
func inline(line string) string {
	line = mdLink.ReplaceAllString(line, "$1")
	line = strings.NewReplacer("**", "", "__", "").Replace(line)
	line = openMark.ReplaceAllString(line, "$1$2")
	line = closeMark.ReplaceAllString(line, "$1$2")
	return strings.Join(strings.Fields(plain(line)), " ")
}
