package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A job log redirects to a signed URL on another host: Tail follows it once,
// without the token, and keeps only the end of the body.
func TestTailFollowsOneRedirectWithoutTheToken(t *testing.T) {
	var hosts []string
	client := New(func(context.Context, string) (string, error) { return "private-value", nil }, Options{Transport: restTransportFunc(func(request *http.Request) (*http.Response, error) {
		hosts = append(hosts, request.URL.Host)
		switch request.URL.Host {
		case "api.github.com":
			if request.Header.Get("Authorization") != "token private-value" {
				t.Error("the API request lacks the token")
			}
			location := "https://logs.example/job/1?sig=x"
			if strings.HasSuffix(request.URL.Path, "/insecure") {
				location = "http://logs.example/job/1"
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		default:
			if request.Header.Get("Authorization") != "" {
				t.Error("the token reached the download host")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 100) + "the end")), Request: request}, nil
		}
	})})
	reader, err := client.REST(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	status, body, err := reader.Tail(context.Background(), "/repos/acme/app/actions/jobs/1/logs", 10)
	if err != nil || status != 200 || string(body) != "xxxthe end" {
		t.Fatalf("status %d body %q err %v", status, body, err)
	}
	if strings.Join(hosts, ",") != "api.github.com,logs.example" {
		t.Fatalf("requests to %v", hosts)
	}
	if _, _, err := reader.Tail(context.Background(), "/repos/acme/app/insecure", 10); err == nil {
		t.Fatal("followed a redirect to plain http")
	}
}
