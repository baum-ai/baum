package userhooks

import "os"

// fileOwner is unknown on Windows, where Geteuid is -1 and the sudo case does
// not arise.
func fileOwner(os.FileInfo) (uint32, bool) { return 0, false }
