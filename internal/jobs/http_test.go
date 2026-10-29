package jobs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// POST /api/jobs with items generates at once; a fork waits for the
// isolation confirmation, which the answer asks for.
func TestHTTPGeneratesDirectly(t *testing.T) {
	q, f := newQueue(t)
	f.mu.Lock()
	f.backend, f.restricted = agent.BackendCursorAgent, true
	f.mu.Unlock()
	handler := q.Handler()
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(body)))
		return w
	}
	items := `"items":[{"clone":"clone","pr":1}],"mode":"single"`
	w := post(`{` + items + `}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"isolation_required":true`) {
		t.Fatalf("fork without confirmation: %d %s", w.Code, w.Body)
	}
	if len(q.List()) != 0 {
		t.Fatalf("queued %+v", q.List())
	}
	w = post(`{` + items + `,"confirm_isolation":true}`)
	var answer struct{ Jobs []Job }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &answer) != nil || len(answer.Jobs) != 1 || answer.Jobs[0].Backend != agent.BackendCursorAgent {
		t.Fatalf("confirmed: %d %s", w.Code, w.Body)
	}
	<-f.started
	close(f.release)
	// In a batch too, the confirmation lets the fork in rather than leaving
	// it out.
	q, f = newQueue(t)
	f.mu.Lock()
	f.restricted = true
	f.mu.Unlock()
	handler = q.Handler()
	w = post(`{"items":[{"clone":"clone","pr":1}],"mode":"batch","confirm_isolation":true}`)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &answer) != nil || len(answer.Jobs) != 1 {
		t.Fatalf("confirmed batch: %d %s", w.Code, w.Body)
	}
	<-f.started
	close(f.release)
}

// A request naming a clone that is gone answers with the needs_clone code,
// apart from the isolation code.
func TestHTTPNeedsClone(t *testing.T) {
	q, f := newQueue(t)
	f.mu.Lock()
	f.prepareErr = fmt.Errorf("prepare: %w", ErrNeedsClone)
	f.mu.Unlock()
	w := httptest.NewRecorder()
	q.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(`{"items":[{"clone":"gone","pr":1}],"mode":"single"}`)))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"needs_clone":true`) || strings.Contains(w.Body.String(), "isolation") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(q.List()) != 0 {
		t.Fatalf("queued %+v", q.List())
	}
}

// Estimates are no longer served, and a start names its items: there is no
// estimate to confirm.
func TestHTTPHasNoEstimates(t *testing.T) {
	q, _ := newQueue(t)
	handler := q.Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/jobs/estimate", strings.NewReader(`{"items":[{"clone":"clone","pr":1}],"mode":"single"}`)))
	if w.Code == http.StatusOK {
		t.Fatalf("estimate answered %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/jobs", strings.NewReader(`{"estimate_id":"e","confirm_estimate":true}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("estimate id answered %d %s", w.Code, w.Body)
	}
	if len(q.List()) != 0 {
		t.Fatalf("queued %+v", q.List())
	}
}
