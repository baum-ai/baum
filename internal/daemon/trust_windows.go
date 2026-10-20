package daemon

import "os"

// secure checks nothing on Windows: owners and permission bits do not
// describe its ACLs, so there only the allowed executables are checked.
func secure(string, os.FileInfo, string) error { return nil }
