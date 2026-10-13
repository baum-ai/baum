package assemble

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/bundle"
)

func TestHTTPImageFetcher(t *testing.T) {
	var sawAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthorization = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("image bytes"))
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
		case "/missing":
			http.NotFound(w, r)
		case "/redirect":
			http.Redirect(w, r, "https://elsewhere.example.test/x", http.StatusFound)
		}
	}))
	defer server.Close()
	fetch := httpImageFetcher(nil, func(u *url.URL) bool { return u.Hostname() == "127.0.0.1" })

	got, err := fetch(context.Background(), server.URL+"/ok?jwt=signed", 50)
	if err != nil || string(got) != "image bytes" {
		t.Fatalf("ok: %q, %v", got, err)
	}
	if sawAuthorization != "" {
		t.Errorf("the download carried credentials: %q", sawAuthorization)
	}
	if _, err := fetch(context.Background(), server.URL+"/big", 50); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("big: %v", err)
	}
	if _, err := fetch(context.Background(), server.URL+"/missing?jwt=signed", 50); err == nil || !strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), "jwt") {
		t.Errorf("missing: %v", err)
	}
	if _, err := fetch(context.Background(), server.URL+"/redirect?jwt=signed", 50); err == nil || strings.Contains(err.Error(), "jwt") || strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("redirect off GitHub's hosts was followed, or leaked: %v", err)
	}
}

func TestHTTPImageFetcherRefusesOtherHosts(t *testing.T) {
	fetch := NewHTTPImageFetcher(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Error("a request was sent to a host that is not GitHub's")
		return nil, errors.New("unexpected request")
	}))
	for _, source := range []string{"https://example.test/a.png", "http://private-user-images.githubusercontent.com/a.png", "file:///etc/passwd", "::"} {
		if _, err := fetch(context.Background(), source, 10); err == nil {
			t.Errorf("%s was fetched", source)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The secrets masked in image alt text count toward the bundle's masked
// spans: bundle.Redact later sees only the masked alt.
func TestCaptureImagesCountsMaskedAltText(t *testing.T) {
	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	found := []bundle.RenderedImage{
		{ID: "att_0123456789abcdef0123456789abcdef", Alt: "diagram by someone@example.com", State: bundle.AttachmentExternal},
		{ID: "att_fedcba9876543210fedcba9876543210", Alt: "plain alt", State: bundle.AttachmentExternal},
	}
	images, _, masked := (&Assembler{}).captureImages(context.Background(), found, redactor, Limits{})
	if masked != 1 || len(images) != 2 || strings.Contains(images[0].Alt, "example.com") {
		t.Fatalf("masked = %d, images = %+v; want the e-mail in the first alt masked and counted once", masked, images)
	}
}
