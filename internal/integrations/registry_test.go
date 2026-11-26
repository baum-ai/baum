package integrations

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

type calendar struct{}

func (calendar) ID() string                        { return "calendar" }
func (calendar) Name() string                      { return "Calendar" }
func (calendar) Status(context.Context) Connection { return Connection{State: "disconnected"} }
func (calendar) Connect(context.Context) (string, error) {
	return "https://calendar.example/connect", nil
}
func (calendar) Disconnect(context.Context) error { return nil }

type tracker struct{ calendar }

func (tracker) ID() string                                    { return "tracker" }
func (tracker) Issues(_ context.Context, text string) []Issue { return []Issue{{Title: text}} }

func TestProviderRoutesAndOptionalResolver(t *testing.T) {
	h := Handler(calendar{}, tracker{})
	for _, tc := range []struct {
		method, path, body, want string
		status                   int
	}{
		{"GET", "", "", `"id":"calendar"`, 200},
		{"POST", "/calendar/connect", "", "https://calendar.example/connect", 200},
		{"POST", "/calendar/disconnect", "", "", 204},
		{"POST", "/issues", `{"description":"example"}`, `"title":"example"`, 200},
		{"POST", "/issues", "oops", "", 400},
		{"POST", "/missing/connect", "", "", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/integrations"+tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: %d %q", tc.path, w.Code, w.Body.String())
		}
	}
}
