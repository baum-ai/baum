package entirecheckpoint

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// HashVerification records whether full.jsonl matches content_hash.txt.
// Mismatch is data for the mapper to surface as a warning, not a parse error.
type HashVerification struct {
	Recorded string `json:"recorded,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Checked  bool   `json:"checked"`
	Mismatch bool   `json:"mismatch"`
}

func VerifyContentHash(full []byte, fullPresent bool, recorded string) HashVerification {
	recorded = strings.TrimSpace(recorded)
	verification := HashVerification{Recorded: recorded}
	if !fullPresent || recorded == "" {
		return verification
	}
	verification.Checked = true
	verification.Actual = ContentHashOf(full)
	verification.Mismatch = verification.Actual != recorded
	return verification
}

// ContentHashOf is content_hash.txt's spelling of data's hash.
func ContentHashOf(data []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}
