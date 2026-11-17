package github

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPullRequestTimelineDecodesEveryEventType(t *testing.T) {
	reply := `{"data":{"repository":{"pullRequest":{"timelineItems":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
	{"__typename":"PullRequestCommit","prCommit":{"oid":"c1","messageHeadline":"Add it","committedDate":"2026-01-01T10:00:00Z","author":{"user":{"login":"alice"}}}},
	{"__typename":"HeadRefForcePushedEvent","createdAt":"2026-01-01T11:00:00Z","actor":{"login":"alice"},"beforeCommit":{"oid":"c1"},"afterCommit":null},
	{"__typename":"BaseRefForcePushedEvent","createdAt":"2026-01-01T11:30:00Z","actor":null,"beforeCommit":{"oid":"b1"},"afterCommit":{"oid":"b2"}},
	{"__typename":"PullRequestReview","createdAt":"2026-01-01T12:00:00Z","submittedAt":"2026-01-01T12:05:00Z","author":{"login":"bob"},"reviewState":"APPROVED","body":"lgtm","url":"https://x/r"},
	{"__typename":"IssueComment","createdAt":"2026-01-01T13:00:00Z","author":{"login":"carol"},"body":"hi","url":"https://x/c"},
	{"__typename":"LabeledEvent","createdAt":"2026-01-01T14:00:00Z","actor":{"login":"bob"},"label":{"name":"bug"}},
	{"__typename":"UnlabeledEvent","createdAt":"2026-01-01T14:01:00Z","actor":{"login":"bob"},"label":{"name":"bug"}},
	{"__typename":"AssignedEvent","createdAt":"2026-01-01T15:00:00Z","actor":{"login":"bob"},"assignee":{"login":"dave"}},
	{"__typename":"UnassignedEvent","createdAt":"2026-01-01T15:01:00Z","actor":{"login":"bob"},"assignee":{"login":"dave"}},
	{"__typename":"ReviewRequestedEvent","createdAt":"2026-01-01T16:00:00Z","actor":{"login":"bob"},"requestedReviewer":{"name":"core"}},
	{"__typename":"ReviewRequestRemovedEvent","createdAt":"2026-01-01T16:01:00Z","actor":{"login":"bob"},"requestedReviewer":{"login":"erin"}},
	{"__typename":"ReadyForReviewEvent","createdAt":"2026-01-01T17:00:00Z","actor":{"login":"alice"}},
	{"__typename":"ConvertToDraftEvent","createdAt":"2026-01-01T17:01:00Z","actor":{"login":"alice"}},
	{"__typename":"BaseRefChangedEvent","createdAt":"2026-01-01T18:00:00Z","actor":{"login":"alice"},"previousRefName":"main","currentRefName":"dev"},
	{"__typename":"RenamedTitleEvent","createdAt":"2026-01-01T18:01:00Z","actor":{"login":"alice"},"previousTitle":"old","currentTitle":"new"},
	{"__typename":"CrossReferencedEvent","createdAt":"2026-01-01T19:00:00Z","actor":{"login":"zed"},"willCloseTarget":true,"source":{"__typename":"PullRequest","number":9,"title":"Other","prState":"OPEN","url":"https://x/9","repository":{"nameWithOwner":"acme/app"}}},
	{"__typename":"CrossReferencedEvent","createdAt":"2026-01-01T19:01:00Z","actor":{"login":"zed"},"willCloseTarget":false,"source":{"__typename":"Issue","number":3,"title":"Bug","issueState":"CLOSED","url":"https://x/3","repository":{"nameWithOwner":"acme/lib"}}},
	{"__typename":"ReferencedEvent","createdAt":"2026-01-01T20:00:00Z","actor":{"login":"zed"},"refCommit":{"oid":"c9"},"commitRepository":{"nameWithOwner":"acme/lib"}},
	{"__typename":"MergedEvent","createdAt":"2026-01-01T21:00:00Z","actor":{"login":"alice"},"mergeCommit":{"oid":"m1"}},
	{"__typename":"ClosedEvent","createdAt":"2026-01-01T21:01:00Z","actor":{"login":"alice"}},
	{"__typename":"ReopenedEvent","createdAt":"2026-01-01T21:02:00Z","actor":{"login":"alice"}},
	{"__typename":"HeadRefDeletedEvent","createdAt":"2026-01-01T21:03:00Z","actor":{"login":"alice"}},
	{"__typename":"SomethingNew","createdAt":"2026-01-01T22:00:00Z"}]}}}}}`
	var sent []string
	client := threadsClient(func(query string, _ map[string]any) string {
		if !strings.Contains(query, "query PullRequestTimeline(") {
			t.Errorf("query %s", query)
		}
		return reply
	}, &sent)
	timeline, err := client.PullRequestTimeline(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 7)
	if err != nil || timeline.Capped {
		t.Fatalf("timeline %v %v", timeline.Capped, err)
	}
	var types []string
	for _, e := range timeline.Events {
		types = append(types, e.Type)
	}
	want := "committed force_pushed base_force_pushed reviewed commented labeled unlabeled assigned unassigned review_requested review_request_removed ready_for_review converted_to_draft base_changed renamed cross_referenced cross_referenced referenced merged closed reopened head_ref_deleted"
	if strings.Join(types, " ") != want {
		t.Fatalf("types %s", types)
	}
	e := timeline.Events
	if e[0].Commit != "c1" || e[0].Headline != "Add it" || e[0].Actor != "alice" || e[0].At.Hour() != 10 {
		t.Errorf("commit %+v", e[0])
	}
	if e[1].Before != "c1" || e[1].After != "" || e[2].Before != "b1" || e[2].After != "b2" {
		t.Errorf("force pushes %+v %+v", e[1], e[2])
	}
	if e[3].State != "APPROVED" || e[3].Body != "lgtm" || e[3].Actor != "bob" || e[3].At.Minute() != 5 || e[3].URL != "https://x/r" {
		t.Errorf("review %+v", e[3])
	}
	if e[4].Body != "hi" || e[5].Label != "bug" || e[7].Assignee != "dave" || e[9].Reviewer != "core" || e[10].Reviewer != "erin" {
		t.Errorf("small events %+v", e[4:11])
	}
	if e[13].From != "main" || e[13].To != "dev" || e[14].From != "old" || e[14].To != "new" {
		t.Errorf("changes %+v %+v", e[13], e[14])
	}
	if s := e[15].Source; s == nil || s.Kind != "pull_request" || s.State != "OPEN" || s.Number != 9 || !e[15].WillClose {
		t.Errorf("cross-reference %+v", e[15])
	}
	if s := e[16].Source; s == nil || s.Kind != "issue" || s.State != "CLOSED" || s.Repository != "acme/lib" || e[16].WillClose {
		t.Errorf("issue reference %+v", e[16])
	}
	if e[17].Commit != "c9" || e[17].Repository != "acme/lib" || e[18].Commit != "m1" {
		t.Errorf("references %+v %+v", e[17], e[18])
	}
}

func TestPullRequestTimelinePagesAndCaps(t *testing.T) {
	var sent []string
	client := threadsClient(func(string, map[string]any) string {
		cursor := "c" + string(rune('0'+len(sent)))
		return `{"data":{"repository":{"pullRequest":{"timelineItems":{"pageInfo":{"hasNextPage":true,"endCursor":"` + cursor + `"},"nodes":[{"__typename":"ClosedEvent","createdAt":"2026-01-01T00:00:00Z"}]}}}}}`
	}, &sent)
	timeline, err := client.PullRequestTimeline(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 7)
	if err != nil || !timeline.Capped || len(timeline.Events) != 5 || len(sent) != 5 {
		t.Fatalf("timeline %+v sent %d %v", timeline, len(sent), err)
	}
}

func TestPullRequestTimelineNotFound(t *testing.T) {
	var sent []string
	client := threadsClient(func(string, map[string]any) string { return `{"data":{"repository":{"pullRequest":null}}}` }, &sent)
	_, err := client.PullRequestTimeline(context.Background(), Repository{Host: "github.com", Owner: "acme", Name: "app"}, 7)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}
