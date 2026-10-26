// Package githubauth owns GitHub.com device authorization and credentials.
package githubauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const ClientID = "Ov23liFSMjrhof9WQlOi"
const Scopes = "repo read:org"

var ErrNotConnected = errors.New("GitHub is not connected")
var ErrStore = errors.New("GitHub credential store unavailable; unlock your operating system credential store and try again")
var ErrRequest = errors.New("GitHub authentication request failed; try again")
var ErrReconnect = errors.New("GitHub authorization expired or was revoked; reconnect GitHub")
var ErrScopes = errors.New("GitHub authorization requires repo and read:org access")
var ErrSSO = errors.New("authorize baum for your organization's GitHub SSO and retry")
var ErrPermissions = errors.New("check repository access and GitHub authorization permissions")

// Credential is daemon-only state and must never be exposed by a public API.
// JSON is reserved for persistence in the operating system credential store.
type Credential struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"`
	RefreshExpiresAt int64  `json:"refresh_expires_at,omitempty"`
	Login            string `json:"login"`
	ID               int64  `json:"id"`
}

// String and GoString keep routine diagnostics from disclosing credentials.
func (Credential) String() string   { return "[GitHub credential]" }
func (Credential) GoString() string { return "[GitHub credential]" }

type Store interface {
	Load(context.Context) (Credential, error)
	Save(context.Context, Credential) error
	Delete(context.Context) error
}
type Device struct {
	ID              string    `json:"id"`
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	ExpiresAt       time.Time `json:"expires_at"`
	Interval        int       `json:"interval"`
}
type Status struct {
	State     string `json:"state"`
	Login     string `json:"login,omitempty"`
	AccountID int64  `json:"account_id,omitempty"`
}
type pending struct {
	public   Device
	response *tokenResponse
	code     string
	next     time.Time
}

// Manager serializes credential refresh, persistence and account replacement.
// Poll performs at most one exchange; callers can poll freely without exceeding
// GitHub's requested interval. Only one device authorization is active at a time.
type Manager struct {
	revision        atomic.Uint64
	healthMu        sync.Mutex
	issuedToken     string
	issuedAccountID int64
	health          error
	identity        context.Context
	identityCancel  context.CancelFunc
	gate            chan struct{}
	store           Store
	client          *http.Client
	oauth, api      string
	now             func() time.Time
	pending         *pending
	rotated         *tokenResponse
	rotatedID       int64
}

type Options struct{ Transport http.RoundTripper }

func New(store Store) *Manager { return NewWithOptions(store, Options{}) }

func NewWithOptions(store Store, opts Options) *Manager {
	identity, cancel := context.WithCancel(context.Background())
	return &Manager{identity: identity, identityCancel: cancel, gate: make(chan struct{}, 1), store: store, client: &http.Client{Transport: opts.Transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, oauth: "https://github.com", api: "https://api.github.com", now: time.Now}
}

// IdentityContext cancels evidence work when the selected account changes.
// Token refresh does not cancel work because it retains the same identity.
func (m *Manager) IdentityContext(ctx context.Context) (context.Context, func()) {
	child, cancel := context.WithCancel(ctx)
	if m.lock(ctx) != nil {
		cancel()
		return child, cancel
	}
	child = context.WithValue(child, identityKey{}, m.identity)
	stop := context.AfterFunc(m.identity, cancel)
	m.unlock()
	return child, func() { stop(); cancel() }
}

type identityKey struct{}

// WithIdentity serializes final local publication with account replacement.
// No network or model work may run inside publish.
func (m *Manager) WithIdentity(ctx context.Context, publish func() error) error {
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.unlock()
	if ctx.Value(identityKey{}) != m.identity {
		return ErrReconnect
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return publish()
}
func (m *Manager) changedIdentity() {
	m.revision.Add(1)
	m.healthMu.Lock()
	m.issuedToken = ""
	m.issuedAccountID = 0
	m.health = nil
	m.healthMu.Unlock()
	m.identityCancel()
	m.identity, m.identityCancel = context.WithCancel(context.Background())
}
func (m *Manager) Revision() uint64 { return m.revision.Load() }

// AccountForToken identifies only the credential most recently issued by Token.
// A late lookup for an older token never inherits the replacement account ID.
func (m *Manager) AccountForToken(token string) int64 {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	if token == "" || token != m.issuedToken {
		return 0
	}
	return m.issuedAccountID
}

// ObserveResponse ignores late failures for a credential no longer in use.
// Upstream bodies and header values are never retained or exposed.
func (m *Manager) ObserveResponse(token string, status int, headers http.Header) {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	if token == "" || token != m.issuedToken {
		return
	}
	switch {
	case status == 401:
		m.health = ErrReconnect
	case status == 403 && headers.Get("X-GitHub-SSO") != "":
		m.health = ErrSSO
	case status == 403 && headers.Get("X-RateLimit-Remaining") != "0" && headers.Get("Retry-After") == "":
		m.health = ErrPermissions
	case status >= 200 && status < 300:
		m.health = nil
	}
}
func (m *Manager) Health() error {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	return m.health
}
func (m *Manager) request(ctx context.Context, endpoint string, form url.Values, token string, out any) (http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	method := http.MethodGet
	var body io.Reader
	if form != nil {
		method = http.MethodPost
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, ErrRequest
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "baum-desktop")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, ErrRequest
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 {
		return nil, ErrReconnect
	}
	if resp.StatusCode != 200 {
		return nil, ErrRequest
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(b) > 64*1024 {
		return nil, ErrRequest
	}
	if json.Unmarshal(b, out) != nil {
		return nil, ErrRequest
	}
	return resp.Header, nil
}
func (m *Manager) Start(ctx context.Context) (Device, error) {
	if err := m.lock(ctx); err != nil {
		return Device{}, err
	}
	defer m.unlock()
	var r struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URI        string `json:"verification_uri"`
		Expires    int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	_, err := m.request(ctx, m.oauth+"/login/device/code", url.Values{"client_id": {ClientID}, "scope": {Scopes}}, "", &r)
	if err != nil {
		return Device{}, err
	}
	if r.DeviceCode == "" || r.UserCode == "" || r.URI != "https://github.com/login/device" || r.Expires <= 0 || r.Expires > 1800 || r.Interval < 1 || r.Interval > 300 {
		return Device{}, ErrRequest
	}
	id := make([]byte, 24)
	if _, err = rand.Read(id); err != nil {
		return Device{}, ErrRequest
	}
	d := Device{hex.EncodeToString(id), r.UserCode, r.URI, m.now().Add(time.Duration(r.Expires) * time.Second), r.Interval}
	m.pending = &pending{public: d, code: r.DeviceCode, next: m.now().Add(time.Duration(r.Interval) * time.Second)}
	return d, nil
}

type tokenResponse struct {
	Access         string `json:"access_token"`
	Refresh        string `json:"refresh_token"`
	Expires        int64  `json:"expires_in"`
	RefreshExpires int64  `json:"refresh_token_expires_in"`
	Error          string `json:"error"`
	TokenType      string `json:"token_type"`
	Interval       int    `json:"interval"`
	received       time.Time
}

func (m *Manager) validate(ctx context.Context, r tokenResponse) (Credential, error) {
	// An OAuth App without token expiration grants a token with no expiry
	// and no refresh token; an expiring one must come with both.
	expiring := r.Expires != 0
	if r.Access == "" || !strings.EqualFold(r.TokenType, "bearer") || r.Expires > 366*24*3600 || r.RefreshExpires > 10*366*24*3600 ||
		(expiring && (r.Expires <= 60 || r.RefreshExpires <= 0 || r.Refresh == "")) {
		return Credential{}, ErrRequest
	}
	var user struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	h, err := m.request(ctx, m.api+"/user", nil, r.Access, &user)
	if err != nil {
		return Credential{}, err
	}
	scopes := map[string]bool{}
	for _, s := range strings.Split(h.Get("X-OAuth-Scopes"), ",") {
		scopes[strings.TrimSpace(s)] = true
	}
	if !scopes["repo"] || (!scopes["read:org"] && !scopes["write:org"] && !scopes["admin:org"]) {
		return Credential{}, ErrScopes
	}
	if user.Login == "" || user.ID <= 0 {
		return Credential{}, ErrRequest
	}
	received := r.received
	if received.IsZero() {
		received = m.now()
	}
	if expiring && m.now().Unix()+60 >= received.Unix()+r.Expires {
		return Credential{}, ErrReconnect
	}
	c := Credential{AccessToken: r.Access, RefreshToken: r.Refresh, Login: user.Login, ID: user.ID}
	if expiring {
		c.ExpiresAt = received.Unix() + r.Expires
	}
	if r.RefreshExpires > 0 {
		c.RefreshExpiresAt = received.Unix() + r.RefreshExpires
	}
	return c, nil
}
func (m *Manager) Poll(ctx context.Context, id string) (Status, error) {
	if err := m.lock(ctx); err != nil {
		return Status{}, err
	}
	defer m.unlock()
	p := m.pending
	if p == nil || p.public.ID != id {
		return Status{State: "cancelled"}, nil
	}
	if p.response != nil {
		c, err := m.validateGrant(ctx, p.response)
		if err != nil {
			return Status{}, err
		}
		// A helper may commit before a cancelled pipe loses its reply. Fence
		// old work before attempting persistence, including ambiguous failures.
		m.changedIdentity()
		if m.store.Save(ctx, c) != nil {
			return Status{}, ErrStore
		}
		m.pending = nil
		m.rotated = nil
		return connected(c), nil
	}
	if !m.now().Before(p.public.ExpiresAt) {
		m.pending = nil
		return Status{State: "expired"}, nil
	}
	if m.now().Before(p.next) {
		return Status{State: "pending"}, nil
	}
	p.next = m.now().Add(time.Duration(p.public.Interval) * time.Second)
	var r tokenResponse
	_, err := m.request(ctx, m.oauth+"/login/oauth/access_token", url.Values{"client_id": {ClientID}, "device_code": {p.code}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, "", &r)
	if err != nil {
		return Status{}, err
	}
	r.received = m.now()
	switch r.Error {
	case "authorization_pending":
		return Status{State: "pending"}, nil
	case "slow_down":
		p.public.Interval += 5
		if r.Interval > 1800 {
			p.next = p.public.ExpiresAt
			return Status{State: "pending"}, nil
		}
		if r.Interval > p.public.Interval {
			p.public.Interval = r.Interval
		}
		p.next = m.now().Add(time.Duration(p.public.Interval) * time.Second)
		return Status{State: "pending"}, nil
	case "access_denied":
		m.pending = nil
		return Status{State: "denied"}, nil
	case "expired_token", "token_expired":
		m.pending = nil
		return Status{State: "expired"}, nil
	case "":
	default:
		m.pending = nil
		return Status{}, ErrRequest
	}
	p.response = &r
	// A successful token exchange consumes the device code. Never retry it if
	// validation or persistence fails, since that could hide a lost credential.
	c, err := m.validateGrant(ctx, &r)
	if err != nil {
		return Status{}, err
	}
	m.changedIdentity()
	if m.store.Save(ctx, c) != nil {
		return Status{}, ErrStore
	}
	m.pending = nil
	m.rotated = nil
	return connected(c), nil
}
func connected(c Credential) Status {
	return Status{State: "connected", Login: c.Login, AccountID: c.ID}
}
func (m *Manager) Cancel(ctx context.Context, id string) error {
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.unlock()
	if m.pending != nil && m.pending.public.ID == id {
		m.pending = nil
	}
	return nil
}
func (m *Manager) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) unlock() { <-m.gate }

func (m *Manager) Status(ctx context.Context) (Status, error) {
	if err := m.lock(ctx); err != nil {
		return Status{}, err
	}
	defer m.unlock()
	c, err := m.store.Load(ctx)
	if err == nil || errors.Is(err, ErrNotConnected) {
		m.healthMu.Lock()
		if errors.Is(m.health, ErrStore) {
			m.health = nil
		}
		m.healthMu.Unlock()
	}
	if errors.Is(err, ErrNotConnected) {
		return Status{State: "disconnected"}, nil
	}
	if err != nil {
		return Status{}, ErrStore
	}
	return connected(c), nil
}
func (m *Manager) Disconnect(ctx context.Context) error {
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.unlock()
	m.pending = nil
	m.rotated = nil
	m.changedIdentity()
	if m.store.Delete(ctx) != nil {
		return ErrStore
	}
	return nil
}
func (m *Manager) Token(ctx context.Context) (token string, returnedErr error) {
	if err := m.lock(ctx); err != nil {
		return "", err
	}
	defer m.unlock()
	var accountID int64
	defer func() {
		m.healthMu.Lock()
		defer m.healthMu.Unlock()
		if returnedErr != nil {
			if !errors.Is(returnedErr, ErrNotConnected) && !errors.Is(returnedErr, context.Canceled) && !errors.Is(returnedErr, context.DeadlineExceeded) {
				m.health = returnedErr
			}
			return
		}
		if token != m.issuedToken {
			m.issuedToken = token
			m.health = nil
		}
		m.issuedAccountID = accountID
	}()
	if m.rotated != nil {
		accountID = m.rotatedID
		return m.persistRotation(ctx)
	}
	c, err := m.store.Load(ctx)
	if errors.Is(err, ErrNotConnected) {
		return "", ErrNotConnected
	}
	if err != nil {
		return "", ErrStore
	}
	accountID = c.ID
	if c.ExpiresAt == 0 || m.now().Unix()+60 < c.ExpiresAt {
		return c.AccessToken, nil
	}
	if c.RefreshToken == "" || (c.RefreshExpiresAt != 0 && m.now().Unix() >= c.RefreshExpiresAt) {
		return "", ErrReconnect
	}
	var r tokenResponse
	_, err = m.request(ctx, m.oauth+"/login/oauth/access_token", url.Values{"client_id": {ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {c.RefreshToken}}, "", &r)
	if err != nil {
		return "", err
	}
	if r.Error != "" {
		// GitHub answered and refused the refresh (a spent token, or a client
		// it will not refresh for); only a new sign-in recovers.
		return "", ErrReconnect
	}
	r.received = m.now()
	m.rotated = &r
	m.rotatedID = c.ID
	return m.persistRotation(ctx)
}
func (m *Manager) persistRotation(ctx context.Context) (string, error) {
	next, err := m.validateGrant(ctx, m.rotated)
	if err != nil {
		return "", err
	}
	if next.ID != m.rotatedID {
		return "", ErrReconnect
	}
	if m.store.Save(ctx, next) != nil {
		return "", ErrStore
	}
	m.rotated = nil
	return next.AccessToken, nil
}

// validateGrant refreshes retained grants against their original receipt time;
// a store outage must never extend an access token's real lifetime.
func (m *Manager) validateGrant(ctx context.Context, r *tokenResponse) (Credential, error) {
	if !r.received.IsZero() && r.Expires > 0 && m.now().Unix()+60 >= r.received.Unix()+r.Expires {
		if r.Refresh == "" || r.RefreshExpires <= 0 || m.now().Unix() >= r.received.Unix()+r.RefreshExpires {
			return Credential{}, ErrReconnect
		}
		var next tokenResponse
		_, err := m.request(ctx, m.oauth+"/login/oauth/access_token", url.Values{"client_id": {ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {r.Refresh}}, "", &next)
		if err != nil {
			return Credential{}, err
		}
		if next.Error != "" {
			return Credential{}, ErrReconnect
		}
		next.received = m.now()
		*r = next
	}
	return m.validate(ctx, *r)
}
