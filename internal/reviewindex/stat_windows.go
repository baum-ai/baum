package reviewindex

import "os"

// Stat through a handle captures the identity immediately. os.Stat defers it
// until os.SameFile, which could then see a replacement at the same path.
// Windows locks belong to handles, so closing this one keeps SQLite's locks.
func statStoreFile(path string) (os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}
