package baumcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/hosted/apiclient"
)

// Environment overrides. BAUM_TOKEN is how CI and MDM-provisioned
// installs authenticate without a credentials file.
const (
	tokenEnvVar  = "BAUM_TOKEN"
	serverEnvVar = "BAUM_SERVER"

	// The API is published under /api on the origin it shares with the UI, so a
	// server URL carries that mount: paths below are appended to it verbatim.
	// This default is the public deployment; a hosted deployment passes --server.
	defaultServerURL = "https://usebaum.com/api"
)

// credentials is the on-disk format of ~/.config/baum/credentials.json.
type credentials struct {
	ServerURL string    `json:"server_url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func credentialsPath() (string, error) {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "baum", "credentials.json"), nil
}

// loadCredentials reads the stored credentials; ok is false when none exist.
func loadCredentials() (creds credentials, ok bool, err error) {
	path, err := credentialsPath()
	if err != nil {
		return credentials{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return credentials{}, false, nil
	}
	if err != nil {
		return credentials{}, false, fmt.Errorf("reading credentials: %w", err)
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentials{}, false, fmt.Errorf("parsing %s: %w", path, err)
	}
	return creds, true, nil
}

// saveCredentials writes the credentials file, readable by the user only.
func saveCredentials(creds credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}
	// A temporary file is created 0600 whatever the existing file's mode, and
	// the rename replaces it atomically so an interrupted write leaves the old
	// file intact.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	return nil
}

// removeCredentials deletes the credentials file. A missing file is not an
// error.
func removeCredentials() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing credentials: %w", err)
	}
	return nil
}

// resolveServerURL picks the server base URL: flag, environment, stored
// credentials, default — in that order.
func resolveServerURL(flagValue string, creds credentials) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv(serverEnvVar); env != "" {
		return env
	}
	if creds.ServerURL != "" {
		return creds.ServerURL
	}
	return defaultServerURL
}

// resolveToken picks the API token for serverURL: environment first, then
// stored credentials. The stored token was issued by the server recorded next
// to it and is never sent anywhere else, so it does not count when serverURL
// names a different deployment. fromFile reports whether the token came from
// the file (and so carries the file's expiry).
func resolveToken(creds credentials, serverURL string) (token string, fromFile bool) {
	if env := os.Getenv(tokenEnvVar); env != "" {
		return env, false
	}
	if creds.Token == "" || !issuedFor(creds, serverURL) {
		return "", false
	}
	return creds.Token, true
}

// issuedFor reports whether the stored token may be sent to serverURL: the
// file's server URL must name the same API, ignoring a trailing slash. Nothing
// else is normalised — a different scheme or host is a different deployment. A
// file that records no server at all predates the check and is trusted.
func issuedFor(creds credentials, serverURL string) bool {
	return creds.ServerURL == "" || strings.TrimSuffix(creds.ServerURL, "/") == strings.TrimSuffix(serverURL, "/")
}

// authenticatedClient builds a client from stored credentials and overrides,
// failing with a login hint when no token is available for the chosen server.
func authenticatedClient(serverFlag string) (*apiclient.Client, credentials, error) {
	creds, _, err := loadCredentials()
	if err != nil {
		return nil, credentials{}, err
	}
	serverURL := resolveServerURL(serverFlag, creds)
	token, _ := resolveToken(creds, serverURL)
	if token == "" {
		return nil, credentials{}, notLoggedInError(creds, serverURL)
	}
	return &apiclient.Client{
		BaseURL:   serverURL,
		Token:     token,
	}, creds, nil
}

// notLoggedInError explains why no token applies: either there is none, or the
// stored one belongs to another server than the one asked for.
func notLoggedInError(creds credentials, serverURL string) error {
	if creds.Token != "" && !issuedFor(creds, serverURL) {
		return fmt.Errorf("logged in to %s, not %s; run `baum login --server %s`", creds.ServerURL, serverURL, serverURL)
	}
	return errors.New("not logged in; run `baum login`")
}
