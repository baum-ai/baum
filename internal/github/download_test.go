package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A job log redirects to a signed URL on another host: Download follows it
// once, without the token, and writes the body.
func TestDownloadFollowsOneRedirectWithoutTheToken(t *testing.T) {
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
	var body strings.Builder
	status, err := reader.Download(context.Background(), "/repos/acme/app/actions/jobs/1/logs", &body)
	if err != nil || status != 200 || body.String() != strings.Repeat("x", 100)+"the end" {
		t.Fatalf("status %d body %q err %v", status, body.String(), err)
	}
	if strings.Join(hosts, ",") != "api.github.com,logs.example" {
		t.Fatalf("requests to %v", hosts)
	}
	if _, err := reader.Download(context.Background(), "/repos/acme/app/insecure", io.Discard); err == nil {
		t.Fatal("followed a redirect to plain http")
	}
}
