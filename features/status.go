package features

// Status is derived from a feature's pull requests (README.md, "Status").
type Status string

const (
	StatusInProgress Status = "in_progress"
	StatusShipped    Status = "shipped"
	StatusAbandoned  Status = "abandoned"
)

// DeriveStatus is in progress while any pull request is open or there is
// none yet, shipped once one merged and none is open, and abandoned when all
// closed unmerged.
func DeriveStatus(pullRequests, open, merged int) Status {
	switch {
	case pullRequests == 0 || open > 0:
		return StatusInProgress
	case merged > 0:
		return StatusShipped
	default:
		return StatusAbandoned
	}
}
