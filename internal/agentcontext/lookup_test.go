package agentcontext

import (
	"context"
	"strings"
	"testing"
)

// A route that names no repository is read only after the record it belongs
// to has been checked: a job's transcript, a group's versions.
func TestActionReaderLooksUpRecordsFirst(t *testing.T) {
	d := &fakeDaemon{}
	reader := NewReader(d, nil).within([]string{"/api/jobs", "/api/groups"}, "acme/web")
	for _, tt := range []struct {
		path    string
		allowed bool
	}{
		{"/api/jobs/j-web/transcript", true},
		{"/api/jobs/j-evil/transcript", false},
		{"/api/jobs/missing/transcript", false},
		{"/api/groups/g-web/versions", true},
		{"/api/groups/g-evil/versions", false},
		{"/api/groups/missing/versions", false},
	} {
		d.reset()
		_, err := reader.Get(context.Background(), tt.path)
		if (err == nil) != tt.allowed {
			t.Errorf("%s: %v", tt.path, err)
		}
		requests := d.requests()
		leaf := strings.Contains(strings.Join(requests, " "), tt.path[strings.LastIndex(tt.path, "/"):])
		if !tt.allowed && leaf {
			t.Errorf("%s was read: %v", tt.path, requests)
		}
		if tt.allowed && len(requests) != 2 {
			t.Errorf("%s: record then route expected, got %v", tt.path, requests)
		}
	}
}
