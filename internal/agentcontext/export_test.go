package agentcontext

// ReaderWithin gives external tests a reader of the allowed routes, bound to
// repository when it is not empty, as a tool's or an action's call has.
func ReaderWithin(r *Reader, allow []string, repository string) *Reader {
	return r.within(allow, repository)
}
