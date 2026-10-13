package githubapi

import (
	"encoding/json"
	"strconv"
)

// BigInt decodes GitHub's BigInt scalar, which arrives as a JSON string or
// number; null decodes as zero.
type BigInt int64

func (value *BigInt) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*value = 0
		return nil
	}
	var number json.Number
	if len(data) > 0 && data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		number = json.Number(text)
	} else {
		number = json.Number(string(data))
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 64)
	*value = BigInt(parsed)
	return err
}

// GitObjectID is a commit SHA passed as a GraphQL variable, typed as GitHub's
// GitObjectID scalar.
type GitObjectID string

func (GitObjectID) GetGraphQLType() string { return "GitObjectID" }
