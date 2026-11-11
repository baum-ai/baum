package githubauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// organizationServer answers like GitHub for an OAuth App token: memberships
// lists every organization the person belongs to, while /user/orgs leaves out
// those that restrict OAuth app access until an owner approves the app.
func organizationServer(t *testing.T, memberships, reachable string, calls *int) *Manager {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Error("missing token")
		}
		*calls++
		switch r.URL.Path {
		case "/user/memberships/orgs":
			if r.URL.Query().Get("state") != "active" {
				t.Error("pending invitations must not count")
			}
			_, _ = fmt.Fprint(w, memberships)
		case "/user/orgs":
			_, _ = fmt.Fprint(w, reachable)
		default:
			t.Error("unexpected URL " + r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	m := New(&memoryStore{})
	m.api = srv.URL
	return m
}

func member(login string) string {
	return fmt.Sprintf(`{"state":"active","role":"member","organization":{"login":%q,"id":1}}`, login)
}

func TestOrganizationsReportsOnesTheTokenCannotReach(t *testing.T) {
	calls := 0
	m := organizationServer(t,
		"["+member("Acme")+","+member("open-co")+","+member("locked")+"]",
		`[{"login":"acme"},{"login":"open-co"}]`, &calls)
	got, err := m.Organizations(context.Background(), "tok", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hidden) != 1 || got.Hidden[0] != "locked" {
		t.Fatalf("hidden = %v", got.Hidden)
	}
	if got.ApprovalURL != "https://github.com/settings/connections/applications/"+ClientID {
		t.Fatal(got.ApprovalURL)
	}
}

func TestOrganizationsNoneHiddenIsEmptyNotNull(t *testing.T) {
	calls := 0
	m := organizationServer(t, "["+member("acme")+"]", `[{"login":"acme"}]`, &calls)
	got, err := m.Organizations(context.Background(), "tok", false)
	if err != nil || got.Hidden == nil || len(got.Hidden) != 0 {
		t.Fatal(got, err)
	}
}

func TestOrganizationsCachedUntilRefreshOrIdentityChange(t *testing.T) {
	calls := 0
	m := organizationServer(t, "["+member("locked")+"]", `[]`, &calls)
	for range 3 {
		if _, err := m.Organizations(context.Background(), "tok", false); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want one read of each list", calls)
	}
	if _, err := m.Organizations(context.Background(), "tok", true); err != nil || calls != 4 {
		t.Fatal(err, calls)
	}
	m.changedIdentity()
	if _, err := m.Organizations(context.Background(), "tok", false); err != nil || calls != 6 {
		t.Fatal(err, calls)
	}
}

func TestOrganizationsFailureIsAnErrorNotAGuess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Resource not accessible"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	m := New(&memoryStore{})
	m.api = srv.URL
	if got, err := m.Organizations(context.Background(), "tok", false); err == nil || len(got.Hidden) != 0 {
		t.Fatal(got, err)
	}
}

func TestOrganizationsReadsEveryPage(t *testing.T) {
	var all []string
	for i := range 130 {
		all = append(all, member(fmt.Sprintf("org-%03d", i)))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/orgs" {
			_, _ = fmt.Fprint(w, `[]`)
			return
		}
		page := r.URL.Query().Get("page")
		if page == "1" {
			_, _ = fmt.Fprint(w, "["+strings.Join(all[:100], ",")+"]")
		} else {
			_, _ = fmt.Fprint(w, "["+strings.Join(all[100:], ",")+"]")
		}
	}))
	t.Cleanup(srv.Close)
	m := New(&memoryStore{})
	m.api = srv.URL
	got, err := m.Organizations(context.Background(), "tok", false)
	if err != nil || len(got.Hidden) != 130 {
		t.Fatal(len(got.Hidden), err)
	}
}

func TestFirstSignInInSSOEnforcingAccountReportsSSO(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = fmt.Fprint(w, `{"device_code":"d","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
		case "/login/oauth/access_token":
			_, _ = fmt.Fprint(w, `{"access_token":"tok","token_type":"bearer"}`)
		case "/user":
			w.Header().Set("X-GitHub-SSO", "required; url=https://github.com/orgs/acme/sso?authorization_request=x")
			http.Error(w, `{"message":"Resource protected by organization SAML enforcement."}`, http.StatusForbidden)
		}
	}))
	t.Cleanup(srv.Close)
	store := &memoryStore{}
	m := New(store)
	m.oauth, m.api = srv.URL, srv.URL
	now := time.Unix(10000, 0)
	m.now = func() time.Time { return now }
	d, err := m.Start(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	if _, err := m.Poll(context.Background(), d.ID); !errors.Is(err, ErrSSO) {
		t.Fatalf("poll error = %v, want ErrSSO", err)
	}
	if store.saves != 0 {
		t.Error("nothing is saved until sign-in completes")
	}
}
