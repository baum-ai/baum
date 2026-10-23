// Package scanskip lists the folders a search for checkouts never enters. The
// daemon's clone finder and `baum setup --all` share it.
package scanskip

import "strings"

// Dir reports directory names a scan never enters: hidden folders,
// dependency and build output, and operating system application folders.
func Dir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "target", "dist", "build", "__pycache__", "Library", "Applications":
		return true
	}
	return false
}

// UserData reports well-known folders of a home directory that hold
// personal files or cloud mounts, not projects. A scan never reads them; it
// applies the check to the home directory's children and to the folders it is
// asked to start from.
func UserData(name string) bool {
	switch name {
	case "Desktop", "Documents", "Downloads", "Pictures", "Movies", "Music", "Public", "Library", "Applications", "iCloud Drive":
		return true
	}
	for _, prefix := range []string{"Google Drive", "Dropbox", "OneDrive"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
