package credentialstore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/proc"
)

// ProcessStore bounds native credential-store prompts by running them in a
// helper process. Secrets travel only through private pipes, never argv or env.
// Every operation waits for the helper to exit before returning, so cancellation
// cannot leave a detached save racing a later disconnect.
type ProcessStore struct{ Executable, Service string }
type storeRequest struct {
	Service    string          `json:"service"`
	Operation  string          `json:"operation"`
	Credential json.RawMessage `json:"credential"`
}
type storeReply struct {
	Credential json.RawMessage `json:"credential"`
	Error      string          `json:"error,omitempty"`
}
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64*1024 {
		return 0, ErrStore
	}
	return b.Buffer.Write(p)
}
func (s ProcessStore) call(ctx context.Context, op string, c json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	executable := s.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, ErrStore
		}
		// A package test binary cannot dispatch the hidden CLI helper. Never
		// re-execute it: that would recursively run its entire suite.
		if strings.HasSuffix(executable, ".test") || strings.HasSuffix(executable, ".test.exe") {
			return nil, ErrNotConnected
		}
		// The helper must be this very executable, even after an update
		// replaced the file at its path.
		if executable, err = selfExecutable(); err != nil {
			return nil, ErrStore
		}
	}
	input, err := json.Marshal(storeRequest{s.Service, op, c})
	if err != nil {
		return nil, ErrStore
	}
	cmd := exec.CommandContext(ctx, executable, "internal", "github-credential")
	proc.Configure(cmd)
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = io.Discard
	var output limitedBuffer
	cmd.Stdout = &output
	if cmd.Run() != nil {
		return nil, ErrStore
	}
	var r storeReply
	if json.Unmarshal(output.Bytes(), &r) != nil {
		return nil, ErrStore
	}
	switch r.Error {
	case "":
		return r.Credential, nil
	case "not_connected":
		return nil, ErrNotConnected
	default:
		return nil, ErrStore
	}
}
func (s ProcessStore) Load(ctx context.Context) (json.RawMessage, error) {
	return s.call(ctx, "load", nil)
}
func (s ProcessStore) Save(ctx context.Context, c json.RawMessage) error {
	_, err := s.call(ctx, "save", c)
	return err
}
func (s ProcessStore) Delete(ctx context.Context) error {
	_, err := s.call(ctx, "delete", nil)
	return err
}

// ServeCredentialHelper is only for the hidden CLI helper. The native store
// runs synchronously here; ProcessStore owns this process's deadline and waits
// for termination before releasing its caller's serialization lock.
func ServeCredentialHelper(ctx context.Context, input io.Reader, output io.Writer) error {
	if err := authorizeCredentialHelper(); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(input, 64*1024+1))
	if err != nil || len(b) > 64*1024 {
		return ErrStore
	}
	var req storeRequest
	if json.Unmarshal(b, &req) != nil {
		return ErrStore
	}
	var reply storeReply
	if !strings.HasPrefix(req.Service, "baum.") || len(req.Service) > 128 {
		return ErrStore
	}
	store := nativeStore{req.Service}
	switch req.Operation {
	case "load":
		reply.Credential, err = store.Load(ctx)
	case "save":
		if !json.Valid(req.Credential) || string(req.Credential) == "null" {
			return ErrStore
		}
		err = store.Save(ctx, req.Credential)
	case "delete":
		err = store.Delete(ctx)
	default:
		return ErrStore
	}
	if err == ErrNotConnected {
		reply.Error = "not_connected"
	} else if err != nil {
		reply.Error = "unavailable"
	}
	if json.NewEncoder(output).Encode(reply) != nil {
		return ErrStore
	}
	return nil
}

// The executable's OS credential-store grant must not become a general CLI
// credential export. Only another instance of this executable may invoke the
// helper, over anonymous pipes created by ProcessStore. Neither argv nor an
// environment variable is evidence of the caller's identity.
func authorizeCredentialHelper() error {
	parent := os.Getppid()
	if parent <= 1 {
		return ErrStore
	}
	path, err := parentExecutable(parent)
	if err != nil {
		return ErrStore
	}
	caller, err := os.Stat(path)
	if err != nil {
		return ErrStore
	}
	executable, err := selfExecutable()
	if err != nil {
		return ErrStore
	}
	self, err := os.Stat(executable)
	if err != nil || !os.SameFile(caller, self) || os.Getppid() != parent {
		return ErrStore
	}
	for _, file := range []*os.File{os.Stdin, os.Stdout} {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return ErrStore
		}
	}
	return nil
}
