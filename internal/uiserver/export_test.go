package uiserver

// SetProposalsBeforeWrite sets the test hook that runs where a proposal's save
// that reads outside its store's transaction has read and not yet written.
func SetProposalsBeforeWrite(p *Proposers, f func()) { p.beforeWrite = f }
