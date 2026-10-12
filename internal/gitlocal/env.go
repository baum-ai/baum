package gitlocal

// ReadEnv is the environment for a read-only command run in the private
// repository at dir: no variable of the caller's that names a repository or
// changes how it is read, no system or global configuration or attributes, no
// replace refs, no lazy fetching, and a home directory with nothing in it.
func ReadEnv(dir string) []string { return mirrorEnv(dir) }
