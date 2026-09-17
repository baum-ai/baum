package session

import (
	"strings"

	"github.com/google/uuid"
)

// idNamespace seeds every minted id. Changing it renames every row in every
// store, so it must never be regenerated.
var idNamespace = uuid.MustParse("8f2b1c1e-6d3a-4f57-9b0e-2a7c5d4e8f10")

// separator is NUL because the parts are vendor strings — attribute keys, file
// paths, prompt ids — and a printable separator lets two different tuples
// produce one key, collapsing two entities into one row.
const separator = "\x00"

// MintID derives a stable id from the identity of the thing being named, so
// re-reading a transcript that has grown keeps the ids of the records it already
// held. kind separates the entity types' id spaces; parts must be the tuple that
// uniquely identifies the entity, which for a stored row is its uniqueness
// constraint.
func MintID(kind string, parts ...string) string {
	key := kind + separator + strings.Join(parts, separator)
	return uuid.NewSHA1(idNamespace, []byte(key)).String()
}
