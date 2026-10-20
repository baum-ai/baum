package apiclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestRequestsCarryTheProtocolAndUserAgent(t *testing.T) {
	var protocol, agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol, agent = r.Header.Get(ProtocolHeader), r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u","github_login":"octocat"}`))
	}))
	defer srv.Close()

	client := &Client{BaseURL: srv.URL, Token: "t", UserAgent: "baum/v1.2.3"}
	if _, err := client.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if protocol != strconv.Itoa(Protocol) || agent != "baum/v1.2.3" {
		t.Errorf("protocol = %q, user agent = %q", protocol, agent)
	}
}

// A server advertising a range this client is outside of is refused with a
// message naming the side to update, before the response is interpreted.
func TestServerOutsideTheProtocolRangeIsRefused(t *testing.T) {
	tests := []struct {
		name     string
		min, max int
		want     string
	}{
		{"client too old", Protocol + 1, Protocol + 2, "too old for"},
		{"client too new", Protocol - 1, Protocol - 1, "too new for"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(MinProtocolHeader, strconv.Itoa(test.min))
				w.Header().Set(MaxProtocolHeader, strconv.Itoa(test.max))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"u","github_login":"octocat"}`))
			}))
			defer srv.Close()

			_, err := (&Client{BaseURL: srv.URL, Token: "t"}).Me(context.Background())
			var incompatible *IncompatibleServerError
			if !errors.As(err, &incompatible) || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("err = %v, want an incompatibility naming %q and the server", err, test.want)
			}
		})
	}
}

func TestServerWithinTheProtocolRangeIsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(MinProtocolHeader, "1")
		w.Header().Set(MaxProtocolHeader, strconv.Itoa(Protocol+3))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := (&Client{BaseURL: srv.URL, Token: "t"}).RevokeCurrentToken(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A server that predates the handshake sends no range. It speaks protocol 1,
// so it serves this client until the client moves past that; a server error
// without a range may be a proxy's and decides nothing.
func TestServerWithoutARangeCountsAsProtocolOne(t *testing.T) {
	plain := func(status int) *http.Response {
		return &http.Response{StatusCode: status, Header: http.Header{}}
	}
	if err := checkProtocolFor(1, plain(http.StatusOK), "https://old.example"); err != nil {
		t.Errorf("protocol 1 client against a legacy server: %v", err)
	}
	err := checkProtocolFor(2, plain(http.StatusOK), "https://old.example")
	var incompatible *IncompatibleServerError
	if !errors.As(err, &incompatible) || !incompatible.Legacy || !strings.Contains(err.Error(), "predates protocol checks") {
		t.Errorf("protocol 2 client against a legacy server: %v", err)
	}
	if err := checkProtocolFor(2, plain(http.StatusBadGateway), "https://old.example"); err != nil {
		t.Errorf("a proxy error decided compatibility: %v", err)
	}
	malformed := plain(http.StatusOK)
	malformed.Header.Set(MinProtocolHeader, "one")
	malformed.Header.Set(MaxProtocolHeader, "1")
	if err := checkProtocolFor(1, malformed, "https://odd.example"); err != nil {
		t.Errorf("a malformed range was trusted: %v", err)
	}
}
