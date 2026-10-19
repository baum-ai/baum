package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type restTransportFunc func(*http.Request) (*http.Response, error)

func (f restTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestRESTHostTokenAndRedirectBoundaries(t *testing.T) {
	tokenCalls := 0
	requests := 0
	client := New(func(_ context.Context, host string) (string, error) {
		tokenCalls++
		if host != "github.com" {
			t.Errorf("token lookup host %s", host)
		}
		return "private-value", nil
	}, Options{Transport: restTransportFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != "https://api.github.com/user" || request.Header.Get("Authorization") != "token private-value" || request.Header.Get("If-None-Match") != `"etag"` {
			t.Errorf("wrong API request %v", request.URL)
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://outside.example/steal"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})})
	if _, err := client.REST(context.Background(), Repository{Host: "outside.example", Owner: "acme", Name: "app"}); err == nil || tokenCalls != 0 {
		t.Fatal("unrecognized host reached credential source")
	}
	reader, err := client.REST(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"https://outside.example/steal", "//outside.example/steal", "/user#fragment"} {
		if _, _, _, err := reader.Read(context.Background(), path, ""); err == nil {
			t.Errorf("accepted unsafe path %s", path)
		}
	}
	if requests != 0 {
		t.Fatal("unsafe relative path made request")
	}
	if _, _, _, err := reader.Read(context.Background(), "/user", `"etag"`); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("redirect error unsafe or missing: %v", err)
	}
	if requests != 1 {
		t.Fatal("followed cross-host redirect")
	}
}

func TestRESTEnterpriseEndpoint(t *testing.T) {
	t.Setenv("GH_HOST", "github.acme.example")
	calls := 0
	client := New(func(_ context.Context, host string) (string, error) {
		if host != "github.acme.example" {
			t.Errorf("token lookup %s", host)
		}
		return "enterprise-token", nil
	}, Options{Transport: restTransportFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != "https://github.acme.example/api/v3/user" || request.Header.Get("Authorization") != "token enterprise-token" {
			t.Errorf("enterprise request %s", request.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"login":"viewer"}`)), Request: request}, nil
	})})
	reader, err := client.REST(context.Background(), Repository{Host: "github.acme.example", Owner: "acme", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := reader.Read(context.Background(), "/user", ""); err != nil || calls != 1 {
		t.Fatalf("enterprise response %v", err)
	}
}
