// Command zipmod writes a module zip, as the module proxy would serve it, from
// the committed files of a git checkout.
//
// It lives in its own module so that golang.org/x/mod does not become a
// dependency of the published one.
//
// Usage: zipmod <module-path> <version> <repo-root> <subdir> <output.zip> <required-file>
//
// subdir is the module directory relative to the repository root: "oss" in the
// private monorepo and "" in the published repository. The command fails when
// the zip lacks required-file, a path relative to the module root.
package main

import (
	"archive/zip"
	"fmt"
	"os"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func main() {
	if len(os.Args) != 7 {
		fmt.Fprintln(os.Stderr, "usage: zipmod <module-path> <version> <repo-root> <subdir> <output.zip> <required-file>")
		os.Exit(2)
	}
	path, version, root, subdir, output, required := os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6]

	out, err := os.Create(output)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zipmod:", err)
		os.Exit(1)
	}
	err = modzip.CreateFromVCS(out, module.Version{Path: path, Version: version}, root, "HEAD", subdir)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = requireFile(output, path+"@"+version+"/"+required)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "zipmod:", err)
		os.Exit(1)
	}
}

// requireFile fails unless the zip holds a file with the given name.
func requireFile(zipPath, name string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name == name {
			return nil
		}
	}
	return fmt.Errorf("%s does not contain %s", zipPath, name)
}
