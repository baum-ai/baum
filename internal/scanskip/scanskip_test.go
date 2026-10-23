package scanskip

import "testing"

func TestNamesAreComparedWithoutCaseOnlyWhereFileSystemsFoldCase(t *testing.T) {
	for _, tc := range []struct {
		goos, name string
		user, dir  bool
	}{
		{"linux", "Documents", true, false},
		{"linux", "documents", false, false},
		{"linux", "Node_Modules", false, false},
		{"darwin", "documents", true, false},
		{"darwin", "DESKTOP", true, false},
		{"darwin", "dropbox (work)", true, false},
		{"darwin", "NODE_MODULES", false, true},
		{"windows", "downloads", true, false},
		{"windows", "appdata", true, false},
		{"linux", "AppData", false, false},
		{"darwin", "AppData", false, false},
		{"linux", "code", false, false},
		{"darwin", ".cache", false, true},
	} {
		if got := UserDataFor(tc.goos, tc.name); got != tc.user {
			t.Errorf("UserDataFor(%s, %q) = %v", tc.goos, tc.name, got)
		}
		if got := DirFor(tc.goos, tc.name); got != tc.dir {
			t.Errorf("DirFor(%s, %q) = %v", tc.goos, tc.name, got)
		}
	}
}
