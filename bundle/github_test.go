package bundle

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestPullRequestStateAndIdentifiers(t *testing.T) {
	if got := PullRequestState("CLOSED", true); got != "merged" {
		t.Errorf("merged state = %q", got)
	}
	if got := PullRequestState("OPEN", false); got != "open" {
		t.Errorf("open state = %q", got)
	}
	if got := CheckRunID(42); got != "check_run:42" {
		t.Errorf("CheckRunID = %q", got)
	}
	if got := StatusCheckID("SC_node", 7); got != "SC_node" {
		t.Errorf("StatusCheckID with node = %q", got)
	}
	if got := StatusCheckID("", 7); got != "status:7" {
		t.Errorf("StatusCheckID without node = %q", got)
	}
	if !IsBotActor(" bot ") || IsBotActor("User") || IsBotActor("") {
		t.Error("IsBotActor recognises only Bot")
	}
}

func TestFlagBotReportNeedsABotAndARegisteredMarker(t *testing.T) {
	const marker = "<!-- coverage-report -->"
	bot := ConversationItem{Bot: true, Body: "Coverage 91%\n" + marker}
	FlagBotReport(&bot, []string{"", marker})
	if !bot.BotReport {
		t.Error("a bot's marked comment is a bot report")
	}
	person := ConversationItem{Body: "Quoting " + marker}
	FlagBotReport(&person, []string{marker})
	unregistered := ConversationItem{Bot: true, Body: marker}
	FlagBotReport(&unregistered, nil)
	if person.BotReport || unregistered.BotReport {
		t.Error("only a bot's comment with a registered marker is a bot report")
	}
}

func TestAttachThreadsPlacesReviewCommentsInTheirThreads(t *testing.T) {
	line := func(n int) *int { return &n }
	b := Bundle{Conversation: []ConversationItem{
		{ID: "11", Kind: "review_comment"},
		{ID: "12", Kind: "review_comment"},
		{ID: "21", Kind: "review_comment"},
		{ID: "11", Kind: "comment"},
		{ID: "99", Kind: "review_comment"},
	}}
	AttachThreads(&b, []ReviewThreadInput{
		{ID: "T_b", Path: "b.go", Side: "RIGHT", Line: line(4), Resolved: true, Comments: []ReviewThreadComment{
			{ID: "11", URL: "https://github.com/acme/widgets/pull/1#discussion_r11"}, {ID: "12"},
		}},
		{ID: "T_a", Path: "a.go", Side: "LEFT", Line: line(9), Comments: []ReviewThreadComment{{ID: "21", URL: "https://example.test/21"}}},
		{ID: "T_c", Path: "c.go", Side: "RIGHT", Line: line(3), Outdated: true},
	})
	want := []Thread{
		{ID: "T_a", Path: "a.go", URL: "https://example.test/21"},
		{ID: "T_b", Path: "b.go", Line: line(4), Resolved: true, URL: "https://github.com/acme/widgets/pull/1#discussion_r11"},
		{ID: "T_c", Path: "c.go", Outdated: true},
	}
	if diff := cmp.Diff(want, b.Threads); diff != "" {
		t.Errorf("threads (-want +got):\n%s", diff)
	}
	resolved, open := true, false
	wantConversation := []ConversationItem{
		{ID: "11", Kind: "review_comment", Thread: "T_b", ThreadRoot: true, Resolved: &resolved},
		{ID: "12", Kind: "review_comment", Thread: "T_b", Resolved: &resolved},
		{ID: "21", Kind: "review_comment", Thread: "T_a", ThreadRoot: true, Resolved: &open},
		{ID: "11", Kind: "comment"},
		{ID: "99", Kind: "review_comment"},
	}
	if diff := cmp.Diff(wantConversation, b.Conversation); diff != "" {
		t.Errorf("conversation (-want +got):\n%s", diff)
	}
	if !b.Sources["review_threads"] {
		t.Error("attached threads are an available source")
	}
}
