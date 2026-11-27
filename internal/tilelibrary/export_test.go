package tilelibrary

import "context"

// Purge runs the sweep every write does first, on its own.
func (s *Store) Purge(ctx context.Context) error {
	return s.write(ctx, func(conn querierExec) error { return purge(ctx, conn, s.now()) })
}

// SetMaxBytes lowers the library's size limit for a test.
func SetMaxBytes(n int64) (restore func()) {
	old := maxBytes
	maxBytes = n
	return func() { maxBytes = old }
}

// SetMaxVersions lowers the versions kept per type for a test.
func SetMaxVersions(n int) (restore func()) {
	old := maxVersions
	maxVersions = n
	return func() { maxVersions = old }
}
