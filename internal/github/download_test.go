package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// tailReader reads a download whose host is given by serve, after the API's
// redirect to it.
func tailReader(t *testing.T, serve func(*http.Request) *http.Response) *RESTReader {
	t.Helper()
	client := New(func(context.Context, string) (string, error) { return "private-value", nil }, Options{Transport: restTransportFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "api.github.com" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://logs.example/job/1"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		}
		response := serve(request)
		response.Request = request
		if response.Header == nil {
			response.Header = http.Header{}
		}
		return response, nil
	})})
	reader, err := client.REST(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

// DownloadTail asks the download host for the end of the file, and says
// when that is all it got.
func TestDownloadTailAsksForTheEnd(t *testing.T) {
	const file = "0123456789"
	reader := tailReader(t, func(r *http.Request) *http.Response {
		if r.Header.Get("Range") != "bytes=-4" {
			t.Errorf("range %q", r.Header.Get("Range"))
		}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": []string{"bytes 6-9/10"}}, Body: io.NopCloser(strings.NewReader(file[len(file)-4:]))}
	})
	var body strings.Builder
	status, partial, err := reader.DownloadTail(context.Background(), "/repos/acme/app/actions/jobs/1/logs", &body, 4)
	if err != nil || status != 200 || !partial || body.String() != "6789" {
		t.Fatalf("status %d partial %v body %q err %v", status, partial, body.String(), err)
	}
}

// A file no longer than the range comes whole in a 206 from byte 0: it is
// not partial, so its first line is kept. A 206 that does not say where it
// starts counts as cut.
func TestDownloadTailOfAFileShorterThanTheRange(t *testing.T) {
	for contentRange, wantPartial := range map[string]bool{
		"bytes 0-9/10": false,
		"bytes 0-9/*":  false,
		"bytes 1-9/10": true,
		"":             true,
		"items 0-9/10": true,
	} {
		reader := tailReader(t, func(*http.Request) *http.Response {
			header := http.Header{}
			if contentRange != "" {
				header.Set("Content-Range", contentRange)
			}
			return &http.Response{StatusCode: 206, Header: header, Body: io.NopCloser(strings.NewReader("one line\n"))}
		})
		var body strings.Builder
		_, partial, err := reader.DownloadTail(context.Background(), "/repos/acme/app/actions/jobs/1/logs", &body, 64)
		if err != nil || partial != wantPartial || body.String() != "one line\n" {
			t.Errorf("%q: partial %v body %q err %v", contentRange, partial, body.String(), err)
		}
	}
}

func TestDownloadTailOfAHostThatIgnoresTheRange(t *testing.T) {
	reader := tailReader(t, func(*http.Request) *http.Response {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("whole file"))}
	})
	var body strings.Builder
	status, partial, err := reader.DownloadTail(context.Background(), "/repos/acme/app/actions/jobs/1/logs", &body, 4)
	if err != nil || status != 200 || partial || body.String() != "whole file" {
		t.Fatalf("status %d partial %v body %q err %v", status, partial, body.String(), err)
	}
	// Over the bound, the wrong part of the file is never passed off as the end.
	reader = tailReader(t, func(*http.Request) *http.Response {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(io.LimitReader(zeros{}, maxDownload+10))}
	})
	if _, _, err := reader.DownloadTail(context.Background(), "/repos/acme/app/actions/jobs/1/logs", io.Discard, 4); err != ErrDownloadTooLarge {
		t.Fatalf("err %v", err)
	}
}

func TestDownloadTailOfAnEmptyFile(t *testing.T) {
	reader := tailReader(t, func(*http.Request) *http.Response {
		return &http.Response{StatusCode: 416, Body: io.NopCloser(strings.NewReader(""))}
	})
	status, partial, err := reader.DownloadTail(context.Background(), "/repos/acme/app/actions/jobs/1/logs", io.Discard, 4)
	if err != nil || status != 200 || partial {
		t.Fatalf("status %d partial %v err %v", status, partial, err)
	}
}

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
