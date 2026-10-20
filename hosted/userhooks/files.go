package userhooks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// geteuid is the effective user, replaced in tests.
var geteuid = os.Geteuid

// refuseForeignHome stops root from editing another user's settings, as
// `sudo baum hooks install --user` would with HOME kept: the files would end up
// owned by root and the user's agent could no longer change them.
func refuseForeignHome(home string) error {
	if geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(home)
	if err != nil {
		return nil
	}
	if owner, ok := fileOwner(info); ok && owner != 0 {
		return fmt.Errorf("refusing to edit the agent settings in %s as root, because that home directory belongs to another user; run the command without sudo", home)
	}
	return nil
}

// stateDir is baum's state directory: $XDG_STATE_HOME/baum, or
// ~/.local/state/baum.
func stateDir(home string) string {
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "baum")
	}
	return filepath.Join(home, ".local", "state", "baum")
}

type settingsFile struct {
	// target is the file written: the settings path, or where it links to,
	// so a settings file kept in a dotfiles repository stays linked.
	target   string
	contents []byte
	existed  bool
	mode     os.FileMode
}

func readSettingsFile(path string) (settingsFile, error) {
	file := settingsFile{target: path, mode: 0o600}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return file, fmt.Errorf("%s is a symbolic link baum cannot follow: %w", path, err)
		}
		file.target = target
		if info, err = os.Stat(target); err != nil {
			return file, fmt.Errorf("inspect %s: %w", target, err)
		}
	}
	if !info.Mode().IsRegular() {
		return file, fmt.Errorf("%s is not a regular file; leaving it unchanged", path)
	}
	if info.Size() > maxSettingsBytes {
		return file, fmt.Errorf("%s is larger than %d bytes; leaving it unchanged", path, maxSettingsBytes)
	}
	contents, err := readBounded(file.target)
	if err != nil {
		return file, fmt.Errorf("read %s: %w", path, err)
	}
	file.contents, file.existed, file.mode = contents, true, info.Mode().Perm()
	return file, nil
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	contents, err := io.ReadAll(io.LimitReader(f, maxSettingsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxSettingsBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxSettingsBytes)
	}
	return contents, nil
}

// unchanged reports an error unless the file still holds what was read: an
// agent that rewrote its settings meanwhile keeps its change, and the
// command can simply be run again.
func (file settingsFile) unchanged() error {
	current, err := readBounded(file.target)
	switch {
	case errors.Is(err, os.ErrNotExist) && !file.existed:
		return nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	case err != nil || !file.existed || !bytes.Equal(current, file.contents):
		return errors.New("the file changed while baum was updating it; run the command again")
	}
	return nil
}

// writeAtomic replaces path with contents through a temporary file in the
// same directory and a rename, so a reader sees the old file or the new one,
// never a partial write. With before set, the rename happens only if the
// file is still as it was read.
func writeAtomic(path string, contents []byte, mode os.FileMode, before *settingsFile) error {
	dir, base := filepath.Split(path)
	temp, err := os.CreateTemp(dir, "."+base+".baum-*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temp.Name())
		}
	}()
	if _, err := temp.Write(contents); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if before != nil {
		if err := before.unchanged(); err != nil {
			return err
		}
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	committed = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
