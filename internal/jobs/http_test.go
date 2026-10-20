package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// POST /api/jobs passes confirm_unpriced through: an unpriced estimate is
// refused without it and queued with it.
func TestHTTPSubmitWiresTheUnpricedAcknowledgement(t *testing.T) {
	q, f := newQueue(t)
	f.mu.Lock()
	f.price, f.backend, f.reason = nil, agent.BackendCursorAgent, "billed_by_cursor_subscription"
	f.mu.Unlock()
	handler := q.Handler()
	post := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return w
	}
	w := post("/api/jobs/estimate", `{"items":[{"clone":"clone","pr":1}],"mode":"single"}`)
	var e Estimate
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &e) != nil || e.Items[0].EstimateReason != "billed_by_cursor_subscription" {
		t.Fatalf("estimate: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"estimate_reason":"billed_by_cursor_subscription"`) || !strings.Contains(w.Body.String(), `"estimate_usd":null`) {
		t.Errorf("estimate JSON: %s", w.Body)
	}
	if w = post("/api/jobs", `{"estimate_id":"`+e.ID+`","confirm_estimate":true,"confirm_isolation":false}`); w.Code != http.StatusConflict {
		t.Fatalf("without confirm_unpriced: %d %s", w.Code, w.Body)
	}
	if w = post("/api/jobs", `{"estimate_id":"`+e.ID+`","confirm_estimate":true,"confirm_isolation":false,"confirm_unpriced":false}`); w.Code != http.StatusConflict {
		t.Fatalf("with confirm_unpriced false: %d %s", w.Code, w.Body)
	}
	if w = post("/api/jobs", `{"estimate_id":"`+e.ID+`","confirm_estimate":true,"confirm_isolation":false,"confirm_unpriced":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"backend":"cursor-agent"`) {
		t.Fatalf("with confirm_unpriced: %d %s", w.Code, w.Body)
	}
	<-f.started
	close(f.release)
}
