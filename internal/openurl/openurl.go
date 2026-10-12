// Package openurl opens a URL in the user's default browser.
package openurl

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// Open starts the platform's URL opener for rawURL and returns without waiting
// for the browser. Only http and https URLs are opened.
func Open(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("refusing to open %q: not an http URL", rawURL)
	}
	name, args, err := command(runtime.GOOS, rawURL)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	// Reap the opener so it does not linger as a zombie.
	go func() { _ = cmd.Wait() }()
	return nil
}

func command(goos, rawURL string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{rawURL}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}, nil
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris":
		return "xdg-open", []string{rawURL}, nil
	}
	return "", nil, errors.New("no known way to open a browser on " + goos)
}
