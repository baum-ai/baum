package uiserver

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/githubauth"
)

// GitHubConnection is the public view of the daemon's own connection. Override
// identifies an explicit credential that remains effective after disconnect.
type GitHubConnection struct {
	State    string             `json:"state"`
	Host     string             `json:"host"`
	Login    string             `json:"login,omitempty"`
	Source   string             `json:"source,omitempty"`
	Override bool               `json:"override"`
	Saved    bool               `json:"saved"`
	Pending  *githubauth.Device `json:"pending,omitempty"`
	Error    string             `json:"error,omitempty"`
	Message  string             `json:"message,omitempty"`
}

// GitHubAuthentication owns the lifetime of polling independently of browser
// requests. Its handler must be mounted inside the local session/CSRF boundary.
type GitHubAuthentication struct {
	ctx      context.Context
	manager  *githubauth.Manager
	cfg      config.Config
	changed  func()
	begin    func() func(bool)
	finish   func(bool)
	finishID string
	op       chan struct{}
	mu       sync.Mutex
	status   GitHubConnection
	cancel   context.CancelFunc
}

func NewGitHubAuthentication(ctx context.Context, manager *githubauth.Manager, cfg config.Config, changed func(), transitions ...func() func(bool)) *GitHubAuthentication {
	a := &GitHubAuthentication{op: make(chan struct{}, 1), ctx: ctx, manager: manager, cfg: cfg, changed: changed, status: GitHubConnection{State: "loading", Host: "github.com"}}
	if len(transitions) > 0 {
		a.begin = transitions[0]
	}
	a.op <- struct{}{}
	go func() {
		defer a.unlock()
		status, err := manager.Status(ctx)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.status.State, a.status.Login, a.status.Saved = status.State, status.Login, status.State == "connected"
		a.setError(err)
	}()
	return a
}

func (a *GitHubAuthentication) setError(err error) {
	a.status.Error, a.status.Message = "", ""
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, githubauth.ErrStore):
		a.status.Error, a.status.Message = "credential_store_unavailable", "Unlock your operating system credential store and try again."
	case errors.Is(err, githubauth.ErrReconnect):
		a.status.Error, a.status.Message = "reconnect_required", "Reconnect GitHub; authorization expired or was revoked."
	case errors.Is(err, githubauth.ErrScopes):
		a.status.Error, a.status.Message = "insufficient_scopes", "GitHub authorization requires repository and organization read access."
	case errors.Is(err, githubauth.ErrSSO):
		a.status.Error, a.status.Message = "sso_required", "Authorize baum for your organization's GitHub SSO and try again."
	case errors.Is(err, githubauth.ErrPermissions):
		a.status.Error, a.status.Message = "permission_denied", "Check repository access and GitHub authorization permissions."
	default:
		a.status.Error, a.status.Message = "auth_request_failed", "GitHub connection failed. Try again."
	}
	if a.status.State == "" || a.status.State == "loading" {
		a.status.State = "disconnected"
	}
}

func (a *GitHubAuthentication) snapshot() GitHubConnection {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.status
	if err := a.manager.Health(); err != nil && s.Pending == nil {
		// Runtime health is composed afresh: an API success clears it without
		// erasing an unrelated device-flow failure.
		previous := a.status
		a.setError(err)
		s = a.status
		a.status = previous
	}
	if _, source := a.cfg.ExplicitGitHubTokenSource("github.com"); source != "" {
		s.Override, s.Source = true, source
	} else if s.Saved {
		s.Source = config.SourceBaum
	}
	return s
}

func (a *GitHubAuthentication) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
}
func (a *GitHubAuthentication) lock(ctx context.Context) bool {
	select {
	case a.op <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
func (a *GitHubAuthentication) unlock() { <-a.op }

// Called only with the operation gate held.
func (a *GitHubAuthentication) complete(changed bool) {
	if a.finish != nil {
		a.finish(changed)
		a.finish = nil
		a.finishID = ""
	}
}

func (a *GitHubAuthentication) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/github-auth", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, a.snapshot()) })
	mux.HandleFunc("POST /api/github-auth/retry", func(w http.ResponseWriter, r *http.Request) {
		if !a.lock(r.Context()) {
			return
		}
		defer a.unlock()
		status, err := a.manager.Status(r.Context())
		a.mu.Lock()
		if a.status.Pending == nil {
			a.status.State, a.status.Login, a.status.Saved = status.State, status.Login, status.State == "connected"
			a.setError(err)
		}
		a.mu.Unlock()
		writeJSON(w, a.snapshot())
	})
	mux.HandleFunc("POST /api/github-auth/start", func(w http.ResponseWriter, r *http.Request) {
		a.stop()
		if !a.lock(r.Context()) {
			return
		}
		defer a.unlock()
		a.complete(false)
		ctx, cancel := context.WithCancel(a.ctx)
		a.mu.Lock()
		previous := a.status.Pending
		a.status.Pending = nil
		a.status.State = "disconnected"
		if a.status.Saved {
			a.status.State = "connected"
		}
		a.cancel = cancel
		a.mu.Unlock()
		if previous != nil {
			_ = a.manager.Cancel(ctx, previous.ID)
		}
		if a.begin != nil {
			a.finish = a.begin()
		}
		device, err := a.manager.Start(ctx)
		a.mu.Lock()
		a.setError(err)
		if err == nil {
			a.status.State, a.status.Pending = "pending", &device
		}
		a.mu.Unlock()
		if err != nil {
			cancel()
			a.complete(false)
		} else {
			a.finishID = device.ID
			a.mu.Lock()
			a.cancel = cancel
			a.mu.Unlock()
			go a.poll(ctx, device.ID)
		}
		writeJSON(w, a.snapshot())
	})
	mux.HandleFunc("POST /api/github-auth/cancel", func(w http.ResponseWriter, r *http.Request) {
		a.stop()
		if !a.lock(r.Context()) {
			return
		}
		defer a.unlock()
		a.complete(false)
		a.mu.Lock()
		pending := a.status.Pending
		a.mu.Unlock()
		var err error
		if pending != nil {
			err = a.manager.Cancel(r.Context(), pending.ID)
		}
		// A cancelled native save may have committed before its reply was lost.
		// Reconcile the durable connection rather than hiding a saved account.
		status, statusErr := a.manager.Status(r.Context())
		if err == nil {
			err = statusErr
		}
		a.mu.Lock()
		a.status.Pending = nil
		a.status.State = "cancelled"
		if statusErr == nil {
			a.status.Saved = status.State == "connected"
			a.status.Login = status.Login
		}
		if a.status.Saved {
			a.status.State = "connected"
		}
		a.setError(err)
		a.mu.Unlock()
		writeJSON(w, a.snapshot())
	})
	mux.HandleFunc("POST /api/github-auth/disconnect", func(w http.ResponseWriter, r *http.Request) {
		a.stop()
		if !a.lock(r.Context()) {
			return
		}
		defer a.unlock()
		a.complete(false)
		if a.begin != nil {
			a.finish = a.begin()
		}
		revision := a.manager.Revision()
		err := a.manager.Disconnect(r.Context())
		changedIdentity := revision != a.manager.Revision()
		a.complete(changedIdentity)
		a.mu.Lock()
		if err == nil {
			a.status = GitHubConnection{State: "disconnected", Host: "github.com"}
		}
		a.setError(err)
		a.mu.Unlock()
		if changedIdentity && a.changed != nil {
			a.changed()
		}
		writeJSON(w, a.snapshot())
	})
	return mux
}

func (a *GitHubAuthentication) poll(ctx context.Context, id string) {
	defer func() {
		if a.lock(context.Background()) {
			if a.finishID == id {
				a.complete(false)
			}
			a.unlock()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// Mutations cancel first and then acquire the manager gate. Never wait for
		// this op lock while holding the manager gate or the status lock.
		if !a.lock(ctx) {
			return
		}
		if ctx.Err() != nil {
			a.unlock()
			return
		}
		a.mu.Lock()
		expired := a.status.Pending != nil && !time.Now().Before(a.status.Pending.ExpiresAt)
		a.mu.Unlock()
		if expired {
			_ = a.manager.Cancel(ctx, id)
			a.mu.Lock()
			a.status.Pending = nil
			a.status.State = "expired"
			a.mu.Unlock()
			a.complete(false)
			a.unlock()
			return
		}
		revision := a.manager.Revision()
		status, err := a.manager.Poll(ctx, id)
		changedIdentity := revision != a.manager.Revision()
		if changedIdentity && err != nil {
			a.complete(true)
			if a.changed != nil {
				a.changed()
			}
			if ctx.Err() == nil && a.begin != nil {
				a.finish = a.begin()
				a.finishID = id
			}
		}
		a.mu.Lock()
		if ctx.Err() != nil && status.State != "connected" {
			a.mu.Unlock()
			a.unlock()
			return
		}
		a.setError(err)
		if err == nil && status.State != "pending" {
			a.status.Pending = nil
			a.status.State = status.State
			if status.State == "connected" {
				a.status.Saved = true
				a.status.Login = status.Login
			}
		}
		a.mu.Unlock()
		if err == nil && status.State != "pending" {
			a.complete(status.State == "connected")
		}
		if err == nil && status.State == "connected" && a.changed != nil {
			a.changed()
		}
		a.unlock()
		if err == nil && status.State != "pending" {
			return
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(4 * time.Second):
			}
		}
	}
}
