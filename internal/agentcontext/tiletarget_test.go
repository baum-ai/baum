package agentcontext

import (
	"encoding/json"
	"strings"
	"testing"
)

// The tile tools act on the pull request the turn was sent from, reading
// the open page's focus only while it is that same pull request.
func TestTileTargetFollowsTheTurn(t *testing.T) {
	pull := func(n int, focus string) PageContext {
		return PageContext{Page: PagePullRequest, Repository: "acme/widgets", PullRequest: n, Focus: json.RawMessage(focus)}
	}
	chat := Consumer{Kind: Chat}
	cases := []struct {
		name    string
		c       CallContext
		pr      int
		section string
		refused string
	}{
		{"no turn: the open page", CallContext{Consumer: chat, Page: pull(7, `{"tab":"sections","section":"sec_a"}`), HasPage: true}, 7, "sec_a", ""},
		{"same pull request: the open page's focus", CallContext{Consumer: chat, Page: pull(7, `{"tab":"overview"}`), HasPage: true, Turn: pull(7, `{"tab":"sections","section":"sec_a"}`), HasTurn: true}, 7, "overview", ""},
		{"same pull request, another case", CallContext{Consumer: chat, Page: PageContext{Page: PagePullRequest, Repository: "ACME/Widgets", PullRequest: 7}, HasPage: true, Turn: pull(7, `{}`), HasTurn: true}, 7, "", ""},
		{"another pull request open: the turn's", CallContext{Consumer: chat, Page: pull(8, `{"tab":"sections","section":"sec_b"}`), HasPage: true, Turn: pull(7, `{"tab":"sections","section":"sec_a"}`), HasTurn: true}, 7, "sec_a", ""},
		{"another page open: the turn's", CallContext{Consumer: chat, Page: PageContext{Page: PageInbox}, HasPage: true, Turn: pull(7, `{"tab":"overview"}`), HasTurn: true}, 7, "overview", ""},
		{"a turn from the inbox", CallContext{Consumer: chat, Page: pull(7, `{}`), HasPage: true, Turn: PageContext{Page: PageInbox}, HasTurn: true}, 0, "", "ask from the pull request's page"},
		{"no page", CallContext{Consumer: chat}, 0, "", "ask from the pull request's page"},
		{"an action", CallContext{Consumer: Consumer{Kind: Action}, Page: pull(7, `{}`), HasPage: true}, 0, "", "ask from the pull request's page"},
	}
	for _, tc := range cases {
		repository, number, err := tilePage(tc.c)
		if tc.refused != "" {
			if err == nil || !strings.Contains(err.Error(), tc.refused) {
				t.Errorf("%s: %s#%d, %v", tc.name, repository, number, err)
			}
			continue
		}
		if err != nil || !strings.EqualFold(repository, "acme/widgets") || number != tc.pr {
			t.Errorf("%s: %s#%d, %v", tc.name, repository, number, err)
		}
		if got := focusSection(tc.c); got != tc.section {
			t.Errorf("%s: section %q, want %q", tc.name, got, tc.section)
		}
	}
}
