package linear

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/credentialstore"
)

type memoryStore struct{ value json.RawMessage }

func (s *memoryStore) Load(context.Context) (json.RawMessage, error) {
	if s.value == nil {
		return nil, credentialstore.ErrNotConnected
	}
	return s.value, nil
}
func (s *memoryStore) Save(_ context.Context, v json.RawMessage) error { s.value = v; return nil }
func (s *memoryStore) Delete(context.Context) error                    { s.value = nil; return nil }

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestConnectLookupRefreshDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := New(ctx, "public")
	p.store = &memoryStore{}
	auth, err := p.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(auth)
	params := u.Query()
	if params.Get("scope") != "read" || params.Get("code_challenge_method") != "S256" {
		t.Fatal("invalid authorization request")
	}
	reads := 0
	p.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		var body string
		if r.URL.Path == "/oauth/token" {
			_ = r.ParseForm()
			if r.Form.Get("grant_type") == "authorization_code" {
				digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(digest[:]) != params.Get("code_challenge") {
					t.Error("wrong verifier")
				}
			} else if r.Form.Get("refresh_token") != "refresh" {
				t.Error("wrong refresh token")
			}
			body = `{"access_token":"access","refresh_token":"refresh","expires_in":86400}`
		} else {
			var req struct{ Query string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Error("missing bearer")
			}
			if strings.Contains(req.Query, "viewer") {
				body = `{"data":{"viewer":{"name":"Sam","organization":{"urlKey":"acme","name":"Acme"}}}}`
			} else {
				reads++
				body = `{"data":{"issue":{"identifier":"A-1","title":"Example","url":"https://linear.app/acme/issue/a-1","state":{"name":"Done"}}}}`
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	for _, s := range []string{"wrong", params.Get("state")} {
		resp, err := http.Get(callback + "?code=code&state=" + s)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if (resp.StatusCode == 200) != (s != "wrong") {
			t.Fatal(resp.StatusCode)
		}
	}
	if st := p.Status(ctx); st.State != "connected" || st.Account != "Sam · Acme" {
		t.Fatal(st)
	}
	p.credential.Expires = time.Now()
	description := "https://linear.app/other/issue/B-1 https://linear.app/acme/issue/a-1/example https://linear.app/acme/issue/A-1"
	for range 2 {
		if issues := p.Issues(ctx, description); len(issues) != 1 || issues[0].State != "Done" {
			t.Fatal(issues)
		}
	}
	if reads != 1 {
		t.Fatal("issue fetch count", reads)
	}
	if err := p.Disconnect(ctx); err != nil || len(p.Issues(ctx, description)) != 0 {
		t.Fatal("disconnect", err)
	}
}
