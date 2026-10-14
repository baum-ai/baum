package review

import (
	"sync"
)

const testHunk = "h_0123456789ab"

func authoredFixture() []byte {
	return []byte(`{"schema":"baum.review/1","summary":{"text":"Changes parser behavior.","review_exchange":{"summary":"No reviews available."},"change_kind":"feature","implements":[],"release_note":null,"breaking_change":null,"attention":null,"authorship":{"declared":"unknown","declared_tools":[],"anchors":[]},"review_minutes":5},"relations":[],"sections":[{"id":"sec_parser","title":"Parser","kind":"logical","hunks":["h_0123456789ab"],"summary":{"text":"Adds the parser.","anchors":[{"type":"hunk","id":"h_0123456789ab"}]},"attention":null,"risks":[],"evidence":[],"blocks":[]}]}`)
}

// sharedValidator compiles the document schema once for the fixtures; a
// validator is read-only after construction.
var sharedValidator = sync.OnceValues(NewDocumentValidator)
