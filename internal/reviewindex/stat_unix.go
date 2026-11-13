//go:build !windows

package reviewindex

import "os"

func statStoreFile(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
