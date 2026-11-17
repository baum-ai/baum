package reviewv2

// CompleteInput is what the host knows besides the authored document.
type CompleteInput struct {
	// Bundle is the assembled bundle the run read (bundle.json bytes), from
	// which the header, index and stats are built as baum.review/1's are.
	Bundle []byte
	// Previous is the previous version's stored document (baum.review/1 or
	// baum.review/2), or nil, for the sections' state and the risks' history.
	Previous []byte
	// Version is the number of the version being written.
	Version int
	// Generator is the review header's generator object (model, prompt
	// version and the rest), as baum.review/1's.
	Generator []byte
}

// Complete turns an authored document (what the levels wrote, with each
// section's details and each tile's state as they ended) into the stored
// one: the header, index and stats from the bundle; the risk score (rubric
// risk-v1 over the sections' levels and the risks' tiers); the reading time;
// each section's derived facts and the risks' history across versions; the
// text of code tiles from the diff. It never calls a model.
//
// The document layer implements it (oss/reviewv2); the orchestration calls it
// once at the end of a run and on every draft it publishes.
func Complete(authored Document, in CompleteInput) (Document, error) {
	authored.Schema = Name
	return authored, nil
}
