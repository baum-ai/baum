// Package daemon runs baum's one long-lived local server per user, which the
// desktop app and coding-agent hooks share, and the client that finds, starts
// and stops it.
//
// A daemon holds an SQLite write lock in its private state directory for its
// whole life; the operating system releases it when the process exits, so a
// crash never leaves a lock behind, and the next daemon clears the socket,
// credential and status files the crashed one left. It offers two transports:
//
//   - control: a versioned line-delimited JSON protocol on a Unix socket only
//     the user can reach (Protocol), used by the baum command line and hooks;
//   - app: the authenticated loopback HTTP server of internal/uiserver, whose
//     credential the app reads from a private file in the state directory.
//
// The daemon never contacts a baum server. What it runs in the background is
// whatever the application started with Options.Start does.
package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// Protocol is the control protocol version. Increment it when a request or
// response changes incompatibly. "hello" and "stop" keep their meaning in
// every version, so any client can identify and replace any daemon.
const Protocol = 1

// Paths locate one user's daemon.
type Paths struct {
	// State is the private directory holding the lock, credential, status
	// file and logs.
	State string
	// Socket is the control socket, in a private directory.
	Socket string
}

// maxSocketPath keeps socket paths within every platform's sun_path (104
// bytes on macOS, 108 on Linux and Windows).
const maxSocketPath = 100

// DefaultPaths are the daemon's state directory, $XDG_STATE_HOME/baum/daemon
// (default ~/.local/state/baum/daemon), and its socket in $XDG_RUNTIME_DIR/baum
// or, without one, in the state directory. A socket path too long for the
// platform moves to a private baum-<uid> directory under the system temporary
// directory.
func DefaultPaths() (Paths, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
		root = filepath.Join(home, ".local", "state")
	}
	p := Paths{State: filepath.Join(root, "baum", "daemon")}
	socketDir := p.State
	if run := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(run) {
		socketDir = filepath.Join(run, "baum")
	}
	p.Socket = filepath.Join(socketDir, "daemon.sock")
	if len(p.Socket) > maxSocketPath && runtime.GOOS != "windows" {
		p.Socket = filepath.Join(os.TempDir(), "baum-"+strconv.Itoa(os.Getuid()), "daemon.sock")
	}
	if len(p.Socket) > maxSocketPath {
		return Paths{}, errors.New("the daemon socket path is too long: " + p.Socket)
	}
	return p, nil
}

func (p Paths) lockFile() string { return filepath.Join(p.State, "daemon.lock.sqlite") }
func (p Paths) startFile() string {
	return filepath.Join(p.State, "start.lock.sqlite")
}
func (p Paths) statusFile() string { return filepath.Join(p.State, "daemon.json") }

// CredentialFile is the private file holding the app transport's address and
// credential while a daemon runs.
func (p Paths) CredentialFile() string { return filepath.Join(p.State, "credential.json") }

// LogFile is the daemon's log; older logs are LogFile()+".1" and ".2".
func (p Paths) LogFile() string { return filepath.Join(p.State, "daemon.log") }

// OutputFile receives a detached daemon's standard output and error, which
// hold only what is written before its log opens or by a crash.
func (p Paths) OutputFile() string { return filepath.Join(p.State, "daemon.out") }

// Status describes a running daemon. It never holds the credential.
type Status struct {
	Protocol  int       `json:"protocol"`
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// App is the loopback origin of the app transport.
	App    string `json:"app,omitempty"`
	Socket string `json:"socket,omitempty"`
	State  string `json:"state,omitempty"`
	// Stopping is set once the daemon has been asked to stop.
	Stopping bool `json:"stopping,omitempty"`
	// RestartPending is set once a newer client asked the daemon to restart
	// as its version when no job is queued or running.
	RestartPending bool `json:"restart_pending,omitempty"`
}

// Uptime is how long the daemon has run, to the second.
func (s Status) Uptime(now time.Time) time.Duration {
	return now.Sub(s.StartedAt).Truncate(time.Second)
}
