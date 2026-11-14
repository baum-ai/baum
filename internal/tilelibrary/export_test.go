package tilelibrary

// SetMaxBytes lowers the library's size limit for a test.
func SetMaxBytes(n int64) (restore func()) {
	old := maxBytes
	maxBytes = n
	return func() { maxBytes = old }
}
