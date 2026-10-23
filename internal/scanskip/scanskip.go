// Package scanskip lists the folders a search for checkouts never enters. The
// daemon's clone finder and `baum setup --all` share it.
package scanskip

import (
	"runtime"
	"strings"
)

// Dir reports directory names a scan never enters: hidden folders,
// dependency and build output, and operating system application folders.
func Dir(name string) bool { return DirFor(runtime.GOOS, name) }

// UserData reports well-known folders of a home directory that hold personal
// files or cloud mounts, not projects. A scan never reads them; it applies the
// check to the home directory's children and to the folders it is asked to
// start from.
func UserData(name string) bool { return UserDataFor(runtime.GOOS, name) }

// foldsCase reports the systems whose file systems ignore case by default.
func foldsCase(goos string) bool { return goos == "darwin" || goos == "windows" }

func equal(goos, a, b string) bool {
	if foldsCase(goos) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func hasPrefix(goos, s, prefix string) bool {
	if foldsCase(goos) {
		return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
	}
	return strings.HasPrefix(s, prefix)
}

// DirFor is Dir for the named operating system (runtime.GOOS values): names
// are compared without regard to case on macOS and Windows.
func DirFor(goos, name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	for _, skipped := range []string{"node_modules", "vendor", "target", "dist", "build", "__pycache__", "Library", "Applications"} {
		if equal(goos, name, skipped) {
			return true
		}
	}
	return false
}

// UserDataFor is UserData for the named operating system. On Windows it also
// lists AppData, which holds application state, not projects.
func UserDataFor(goos, name string) bool {
	for _, folder := range []string{"Desktop", "Documents", "Downloads", "Pictures", "Movies", "Music", "Public", "Library", "Applications", "iCloud Drive"} {
		if equal(goos, name, folder) {
			return true
		}
	}
	if goos == "windows" && equal(goos, name, "AppData") {
		return true
	}
	for _, prefix := range []string{"Google Drive", "Dropbox", "OneDrive"} {
		if hasPrefix(goos, name, prefix) {
			return true
		}
	}
	return false
}
