package locallink

import (
	"strings"
	"testing"

	"github.com/baumai/baum/internal/locallink/linktest"
)

func TestResolveBoundsRemoteNames(t *testing.T) {
	f := linktest.New(t)
	long := strings.Repeat("n", 200)
	linktest.Git(t, f.Repo, "remote", "set-url", "origin", "https://github.com/x/y.git")
	linktest.Git(t, f.Repo, "remote", "add", long, "https://github.com/x/z.git")
	result := resolve(t, request(f))
	if result.Code != CodeRemoteMismatch || strings.Contains(result.Reason, strings.Repeat("n", 41)) || !strings.Contains(result.Reason, "… → x/z") {
		t.Errorf("reason = %q", result.Reason)
	}
}
