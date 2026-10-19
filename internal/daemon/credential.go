package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/baum-ai/baum/internal/privatefs"
)

// Credential is how an app client reaches the daemon's HTTP transport. URL is
// the launch URL, http://127.0.0.1:PORT/#token=<secret>: opening it runs the
// bootstrap page, which exchanges the fragment for the session cookie. The
// secret also works as a Bearer token on /api/ routes. It stays valid for
// repeated exchanges for the daemon's whole life and changes only when a new
// daemon starts, which rewrites the file atomically.
type Credential struct {
	URL string `json:"url"`
}

// Origin is the credential's loopback origin, http://127.0.0.1:PORT.
func (c Credential) Origin() string {
	origin, _, _ := strings.Cut(c.URL, "/#token=")
	return origin
}

// Token is the credential's secret.
func (c Credential) Token() string {
	_, token, _ := strings.Cut(c.URL, "/#token=")
	return token
}

// ErrNoCredential means no daemon has published a credential.
var ErrNoCredential = errors.New("the baum daemon has not published an app credential")

// ReadCredential reads the running daemon's credential. The file must be a
// small private regular file holding a loopback launch URL.
func ReadCredential(paths Paths) (Credential, error) {
	path := paths.CredentialFile()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, ErrNoCredential
	}
	if err != nil {
		return Credential{}, err
	}
	if !privatefs.PrivateFile(path, info) || info.Size() > 4096 {
		return Credential{}, fmt.Errorf("%s must be a small private (0600) file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Credential{}, err
	}
	var c Credential
	if err = json.Unmarshal(data, &c); err != nil {
		return Credential{}, fmt.Errorf("%s: %w", path, err)
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/" || u.RawQuery != "" || u.User != nil ||
		len(c.Token()) != 43 || c.Origin()+"/#token="+c.Token() != c.URL {
		return Credential{}, fmt.Errorf("%s does not hold a loopback launch URL", path)
	}
	return c, nil
}

// writePrivate atomically replaces path with value as JSON, mode 0600, in a
// private directory.
func writePrivate(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = privatefs.ProtectDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	// CreateTemp makes the file 0600 before anything is written to it.
	f, err := os.CreateTemp(filepath.Dir(path), ".baum-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}
