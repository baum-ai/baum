package githubauth

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
type ProcessStore struct{ Executable string }
type storeRequest struct {
	Operation  string     `json:"operation"`
	Credential Credential `json:"credential"`
}
type storeReply struct {
	Credential Credential `json:"credential"`
	Error      string     `json:"error,omitempty"`
}
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64*1024 {
		return 0, ErrStore
	}
	return b.Buffer.Write(p)
}
func (s ProcessStore) call(ctx context.Context, op string, c Credential) (Credential, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	executable := s.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return Credential{}, ErrStore
		}
		// A package test binary cannot dispatch the hidden CLI helper. Never
		// re-execute it: that would recursively run its entire suite.
		if strings.HasSuffix(executable, ".test") || strings.HasSuffix(executable, ".test.exe") {
			return Credential{}, ErrNotConnected
		}
	}
	input, err := json.Marshal(storeRequest{op, c})
	if err != nil {
		return Credential{}, ErrStore
	}
	cmd := exec.CommandContext(ctx, executable, "internal", "github-credential")
	proc.Configure(cmd)
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = io.Discard
	var output limitedBuffer
	cmd.Stdout = &output
	if cmd.Run() != nil {
		return Credential{}, ErrStore
	}
	var r storeReply
	if json.Unmarshal(output.Bytes(), &r) != nil {
		return Credential{}, ErrStore
	}
	switch r.Error {
	case "":
		return r.Credential, nil
	case "not_connected":
		return Credential{}, ErrNotConnected
	default:
		return Credential{}, ErrStore
	}
}
func (s ProcessStore) Load(ctx context.Context) (Credential, error) {
	return s.call(ctx, "load", Credential{})
}
func (s ProcessStore) Save(ctx context.Context, c Credential) error {
	_, err := s.call(ctx, "save", c)
	return err
}
func (s ProcessStore) Delete(ctx context.Context) error {
	_, err := s.call(ctx, "delete", Credential{})
	return err
}

// ServeCredentialHelper is only for the hidden CLI helper. The native store
// runs synchronously here; ProcessStore owns this process's deadline and waits
// for termination before releasing its caller's serialization lock.
func ServeCredentialHelper(ctx context.Context, input io.Reader, output io.Writer) error {
	b, err := io.ReadAll(io.LimitReader(input, 64*1024+1))
	if err != nil || len(b) > 64*1024 {
		return ErrStore
	}
	var req storeRequest
	if json.Unmarshal(b, &req) != nil {
		return ErrStore
	}
	var reply storeReply
	store := nativeStore{}
	switch req.Operation {
	case "load":
		reply.Credential, err = store.Load(ctx)
	case "save":
		if req.Credential.AccessToken == "" || req.Credential.Login == "" || req.Credential.ID <= 0 {
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
