// Package linear connects the local daemon to a user's Linear workspace.
package linear

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/credentialstore"
	"github.com/baum-ai/baum/internal/integrations"
)

const callback = "http://127.0.0.1:37841/oauth/linear/callback"

// releaseClientID is the public OAuth application ID embedded by release builds.
var releaseClientID string

var errRequest = errors.New("Linear request failed")
var errReconnect = errors.New("Linear authorization expired")

type store interface {
	Load(context.Context) (json.RawMessage, error)
	Save(context.Context, json.RawMessage) error
	Delete(context.Context) error
}
type credential struct {
	Access    string
	Refresh   string
	Expires   time.Time
	Account   string
	Workspace string
}
type Provider struct {
	// A rotated token must survive a temporary OS keyring failure.
	rotated    *credential
	mu         sync.Mutex
	lifetime   context.Context
	clientID   string
	client     *http.Client
	store      store
	credential *credential
	pending    *http.Server
	message    string
	reconnect  bool
	cache      map[string]cachedIssue
}

func New(ctx context.Context, clientID string) *Provider {
	return &Provider{lifetime: ctx, clientID: cmp.Or(clientID, releaseClientID), client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, store: credentialstore.ProcessStore{Service: "baum.linear.app"}}
}
func (*Provider) ID() string   { return "linear" }
func (*Provider) Name() string { return "Linear" }
func (p *Provider) load(ctx context.Context) error {
	if p.credential != nil {
		return nil
	}
	raw, err := p.store.Load(ctx)
	if err != nil {
		return err
	}
	var c credential
	if json.Unmarshal(raw, &c) != nil || c.Access == "" || c.Refresh == "" || c.Workspace == "" {
		return credentialstore.ErrStore
	}
	p.credential = &c
	return nil
}
func (p *Provider) save(ctx context.Context, c credential) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err = p.store.Save(ctx, raw); err != nil {
		return err
	}
	p.credential = &c
	p.rotated = nil
	return nil
}
func (p *Provider) Status(ctx context.Context) integrations.Connection {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := integrations.Connection{State: "disconnected", Message: p.message}
	if p.clientID == "" {
		status.State = "unconfigured"
		status.Message = "Configure the Linear OAuth application to connect."
		return status
	}
	if p.pending != nil {
		status.State = "pending"
		return status
	}
	if err := p.load(ctx); err == nil {
		status.Account = p.credential.Account
		status.State = "connected"
		if p.reconnect {
			status.State = "reconnect"
		}
	} else if !errors.Is(err, credentialstore.ErrNotConnected) {
		status.State = "unavailable"
		status.Message = "Unlock your operating system credential store and try again."
	}
	return status
}
func (p *Provider) Disconnect(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stop()
	if err := p.store.Delete(ctx); err != nil {
		return err
	}
	p.credential = nil
	p.rotated = nil
	p.cache = nil
	p.message = ""
	p.reconnect = false
	return nil
}
func (p *Provider) stop() {
	if p.pending != nil {
		_ = p.pending.Close()
		p.pending = nil
	}
}
func (p *Provider) Connect(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clientID == "" {
		return "", errRequest
	}
	p.stop()
	listener, err := net.Listen("tcp4", "127.0.0.1:37841")
	if err != nil {
		return "", err
	}
	state, verifier := rand.Text(), rand.Text()+rand.Text()
	digest := sha256.Sum256([]byte(verifier))
	params := url.Values{"client_id": {p.clientID}, "redirect_uri": {callback}, "scope": {"read"}, "response_type": {"code"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 45 * time.Second, MaxHeaderBytes: 8192}
	ctx, cancel := context.WithTimeout(p.lifetime, 5*time.Minute)
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/oauth/linear/callback" || r.Host != "127.0.0.1:37841" || r.URL.Query().Get("state") != state {
			http.NotFound(w, r)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.pending != server {
			http.Error(w, "Sign-in expired", 410)
			return
		}
		p.pending = nil
		defer cancel()
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if err := p.finish(ctx, verifier, r.URL.Query().Get("code")); err != nil {
			p.message = "Sign-in did not complete. Try connecting again."
			http.Error(w, p.message, 400)
			return
		}
		p.cache = nil
		p.message = ""
		p.reconnect = false
		_, _ = fmt.Fprintln(w, "Linear connected. You can return to baum.")
	})
	p.pending = server
	p.message = ""
	go func() { _ = server.Serve(listener); cancel() }()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.pending == server {
			p.pending = nil
			p.message = "Sign-in expired. Connect again."
		}
	}()
	return "https://linear.app/oauth/authorize?" + params.Encode(), nil
}
func (p *Provider) finish(ctx context.Context, verifier, code string) error {
	if code == "" {
		return errRequest
	}
	c, err := p.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {callback}})
	if err != nil {
		return err
	}
	var data struct {
		Viewer struct {
			Name         string
			Organization struct{ Name, URLKey string }
		}
	}
	if err = p.query(ctx, c.Access, "{ viewer { name organization { name urlKey } } }", nil, &data); err != nil {
		return err
	}
	c.Workspace = data.Viewer.Organization.URLKey
	if c.Workspace == "" {
		return errRequest
	}
	c.Account = data.Viewer.Name + " · " + data.Viewer.Organization.Name
	return p.save(ctx, c)
}
func (p *Provider) exchange(ctx context.Context, form url.Values) (credential, error) {
	form.Set("client_id", p.clientID)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.linear.app/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return credential{}, errRequest
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return credential{}, errRequest
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		Error   string
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&out) != nil {
		return credential{}, errRequest
	}
	if out.Error == "invalid_grant" {
		return credential{}, errReconnect
	}
	if resp.StatusCode != 200 || out.Access == "" || out.Refresh == "" || out.Expires <= 0 {
		return credential{}, errRequest
	}
	return credential{Access: out.Access, Refresh: out.Refresh, Expires: time.Now().Add(time.Duration(out.Expires) * time.Second)}, nil
}
func (p *Provider) token(ctx context.Context) (string, error) {
	c := p.credential
	if p.rotated != nil {
		c = p.rotated
	}
	if time.Until(c.Expires) > time.Minute {
		if p.rotated != nil {
			if err := p.save(ctx, *c); err != nil {
				return "", err
			}
		}
		return c.Access, nil
	}
	next, err := p.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.Refresh}})
	if err != nil {
		return "", err
	}
	next.Account, next.Workspace = c.Account, c.Workspace
	p.rotated = &next
	if err := p.save(ctx, next); err != nil {
		return "", err
	}
	return next.Access, nil
}
