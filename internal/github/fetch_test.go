package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/baum-ai/baum/bundle"
)

const (
	headSHA   = "1111111111111111111111111111111111111111"
	baseSHA   = "2222222222222222222222222222222222222222"
	movedSHA  = "3333333333333333333333333333333333333333"
	commitOne = "4444444444444444444444444444444444444444"
	uploadURL = "https://github.com/user-attachments/assets/0f0e0d0c-0b0a-4908-8706-050403020100"
	signedURL = "https://private-user-images.githubusercontent.com/1/0f0e0d0c-0b0a-4908-8706-050403020100.png?jwt=signed"
)

var acme = Repository{Host: "github.com", Owner: "acme", Name: "widgets"}

func at(clock string) string { return "2026-01-02T" + clock + "Z" }

func when(clock string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, at(clock))
	if err != nil {
		panic(err)
	}
	return &parsed
}

func user(login string) map[string]any { return map[string]any{"login": login, "__typename": "User"} }
func bot(login string) map[string]any  { return map[string]any{"login": login, "__typename": "Bot"} }

func pullRequestData(head string) map[string]any {
	return map[string]any{"repository": map[string]any{
		"nameWithOwner": "Acme/widgets",
		"pullRequest": map[string]any{
			"number": 7, "state": "OPEN", "isDraft": false, "title": "Add widget sizes",
			"body":              "Sizes for widgets.\n\n## Screens\n\n![Sizes](" + uploadURL + ")",
			"bodyHTML":          `<p>Sizes for widgets.</p><h2>Screens</h2><p><img src="` + signedURL + `" alt="Sizes"></p>`,
			"createdAt":         at("03:00:00"),
			"author":            user("octo-dev"),
			"authorAssociation": "MEMBER",
			"baseRefName":       "main", "baseRefOid": baseSHA, "headRefName": "feature/sizes", "headRefOid": head,
			"merged": false, "mergeCommit": nil,
		},
	}}
}

func prConnection(head, field string, connection map[string]any) fakeResponse {
	return data(map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"headRefOid": head, field: connection}}})
}

func reviewCommentNode(id, login, typename, body, path, clock, reviewID string) map[string]any {
	return map[string]any{
		"fullDatabaseId": id, "state": "SUBMITTED", "authorAssociation": "MEMBER", "author": map[string]any{"login": login, "__typename": typename},
		"body": body, "path": path, "url": "https://github.com/acme/widgets/pull/7#discussion_r" + id,
		"createdAt": at(clock), "pullRequestReview": map[string]any{"fullDatabaseId": reviewID},
	}
}

// draftCommentNode is the token owner's own pending review comment.
func draftCommentNode(id, path, clock string) map[string]any {
	node := reviewCommentNode(id, "token-owner", "User", "Draft, not yet submitted.", path, clock, "299")
	node["state"] = "PENDING"
	return node
}

func checkRunNode(id int, name, status string, conclusion any) map[string]any {
	return map[string]any{
		"databaseId": id, "name": name, "status": status, "conclusion": conclusion,
		"permalink": "https://github.com/acme/widgets/runs/" + strconv.Itoa(id),
		"startedAt": at("04:00:00"), "completedAt": nil,
	}
}

// fixtureRoutes is one synthetic pull request whose every connection spans
// more than one page.
func fixtureRoutes(t *testing.T) map[string]route {
	return map[string]route{
		"PullRequest": func(gqlRequest, int) fakeResponse { return data(pullRequestData(headSHA)) },
		"PullRequestReviewRequests": pages(t, map[string]func() fakeResponse{
			"": func() fakeResponse {
				return prConnection(headSHA, "reviewRequests", page([]any{
					map[string]any{"requestedReviewer": map[string]any{"__typename": "User", "login": "reviewer-one"}},
				}, "requests-1"))
			},
			"requests-1": func() fakeResponse {
				return prConnection(headSHA, "reviewRequests", page([]any{
					map[string]any{"requestedReviewer": map[string]any{"__typename": "Team", "slug": "platform-team"}},
					map[string]any{"requestedReviewer": nil},
				}, ""))
			},
		}),
		"PullRequestCommits": pages(t, map[string]func() fakeResponse{
			"": func() fakeResponse {
				return prConnection(headSHA, "commits", page([]any{map[string]any{"commit": map[string]any{
					"oid": commitOne, "message": "Add sizes\n\nWith a longer body.", "committedDate": at("02:00:00"),
					"parents": map[string]any{"nodes": []any{map[string]any{"oid": baseSHA}}},
				}}}, "commits-1"))
			},
			"commits-1": func() fakeResponse {
				return prConnection(headSHA, "commits", page([]any{map[string]any{"commit": map[string]any{
					"oid": headSHA, "message": "Fix size rounding", "committedDate": at("04:00:00"),
					"parents": map[string]any{"nodes": []any{map[string]any{"oid": commitOne}}},
				}}}, ""))
			},
		}),
		"PullRequestComments": pages(t, map[string]func() fakeResponse{
			"": func() fakeResponse {
				return prConnection(headSHA, "comments", page([]any{
					map[string]any{"fullDatabaseId": "101", "authorAssociation": "MEMBER", "author": user("octo-dev"), "body": "Ready for review.", "createdAt": at("03:10:00")},
					map[string]any{"fullDatabaseId": "102", "authorAssociation": "NONE", "author": bot("baum-app"), "body": "\n" + bundle.SummaryCommentMarker + "\nSummary", "createdAt": at("03:11:00")},
				}, "comments-1"))
			},
			"comments-1": func() fakeResponse {
				return prConnection(headSHA, "comments", page([]any{
					map[string]any{"fullDatabaseId": "103", "authorAssociation": "MEMBER", "author": user("octo-reviewer"), "body": bundle.SummaryCommentMarker + " quoted by a person", "createdAt": at("03:12:00")},
					map[string]any{"fullDatabaseId": "104", "authorAssociation": "NONE", "author": bot("copilot-reviewer"), "body": "Looks fine.", "createdAt": at("03:13:00")},
					map[string]any{"fullDatabaseId": "105", "authorAssociation": "MEMBER", "author": user("octo-dev"), "body": "Generated with Claude Code", "createdAt": at("03:14:00")},
					map[string]any{"fullDatabaseId": nil, "authorAssociation": "MEMBER", "author": user("ghost"), "body": "no id", "createdAt": at("03:15:00")},
				}, ""))
			},
		}),
		"PullRequestReviews": pages(t, map[string]func() fakeResponse{
			"": func() fakeResponse {
				return prConnection(headSHA, "reviews", page([]any{
					map[string]any{"fullDatabaseId": "201", "authorAssociation": "MEMBER", "author": user("octo-reviewer"), "state": "CHANGES_REQUESTED", "body": "Rounding is off.", "commit": map[string]any{"oid": commitOne}, "submittedAt": at("03:20:00")},
					map[string]any{"fullDatabaseId": "202", "authorAssociation": "MEMBER", "author": user("octo-reviewer"), "state": "PENDING", "body": "draft", "commit": nil, "submittedAt": nil},
				}, ""))
			},
		}),
		"PullRequestReviewThreads": pages(t, map[string]func() fakeResponse{
			"": func() fakeResponse {
				return prConnection(headSHA, "reviewThreads", page([]any{map[string]any{
					"id": "PRRT_1", "path": "size.go", "diffSide": "RIGHT", "line": 12, "isResolved": true, "isOutdated": false,
					"comments": page([]any{
						reviewCommentNode("301", "octo-reviewer", "User", "Round half up.", "size.go", "03:21:00", "201"),
						draftCommentNode("304", "size.go", "03:22:00"),
					}, "thread-comments-1"),
				}}, "threads-1"))
			},
			"threads-1": func() fakeResponse {
				return prConnection(headSHA, "reviewThreads", page([]any{map[string]any{
					"id": "PRRT_2", "path": "old.go", "diffSide": "LEFT", "line": 3, "isResolved": false, "isOutdated": false,
					"comments": page([]any{reviewCommentNode("303", "cursor-bugbot", "Bot", "Possible nil dereference.", "old.go", "03:23:00", "203")}, ""),
				}, map[string]any{
					"id": "PRRT_3", "path": "draft.go", "diffSide": "RIGHT", "line": 8, "isResolved": false, "isOutdated": false,
					"comments": page([]any{draftCommentNode("305", "draft.go", "03:24:00")}, ""),
				}}, ""))
			},
		}),
		"ReviewThreadComments": func(request gqlRequest, _ int) fakeResponse {
			if request.Variables["id"] != "PRRT_1" || request.cursor() != "thread-comments-1" {
				t.Errorf("thread comments requested for %v after %q", request.Variables["id"], request.cursor())
			}
			return data(map[string]any{"node": map[string]any{"id": "PRRT_1", "comments": page([]any{
				reviewCommentNode("302", "octo-dev", "User", "Done.", "size.go", "03:30:00", "204"),
			}, "")}})
		},
		"HeadChecks": func(request gqlRequest, _ int) fakeResponse {
			if request.Variables["oid"] != headSHA {
				t.Errorf("checks requested for %v, want the head", request.Variables["oid"])
			}
			switch request.cursor() {
			case "":
				return data(map[string]any{"repository": map[string]any{"object": map[string]any{
					"oid": headSHA,
					"checkSuites": page([]any{map[string]any{"id": "CS_1", "databaseId": 1, "checkRuns": page([]any{
						checkRunNode(11, "build", "COMPLETED", "FAILURE"),
						checkRunNode(12, "build", "COMPLETED", "SUCCESS"),
					}, "runs-1")}}, "suites-1"),
					"status": map[string]any{"contexts": []any{map[string]any{
						"id": "SC_1", "context": "ci/legacy", "state": "SUCCESS", "targetUrl": "https://ci.example.test/1", "createdAt": at("04:05:00"),
					}}},
				}}})
			case "suites-1":
				return data(map[string]any{"repository": map[string]any{"object": map[string]any{
					"oid":         headSHA,
					"checkSuites": page([]any{map[string]any{"id": "CS_2", "databaseId": 2, "checkRuns": page([]any{checkRunNode(21, "lint", "IN_PROGRESS", nil)}, "")}}, ""),
					"status":      map[string]any{"contexts": []any{}},
				}}})
			}
			t.Errorf("unexpected check suites cursor %q", request.cursor())
			return fakeResponse{status: http.StatusBadRequest, body: map[string]any{}}
		},
		"CheckSuiteRuns": func(request gqlRequest, _ int) fakeResponse {
			if request.Variables["id"] != "CS_1" || request.cursor() != "runs-1" {
				t.Errorf("check runs requested for %v after %q", request.Variables["id"], request.cursor())
			}
			return data(map[string]any{"node": map[string]any{"id": "CS_1", "checkRuns": page([]any{checkRunNode(13, "test", "COMPLETED", "SUCCESS")}, "")}})
		},
		"RequiredChecks": func(request gqlRequest, _ int) fakeResponse {
			if request.Variables["ref"] != "refs/heads/main" {
				t.Errorf("protection requested for %v, want the base branch", request.Variables["ref"])
			}
			return data(map[string]any{"repository": map[string]any{"ref": map[string]any{
				"refUpdateRule": map[string]any{"requiredStatusCheckContexts": []any{"build", nil}},
				"rules": page([]any{
					map[string]any{"type": "REQUIRED_STATUS_CHECKS", "parameters": map[string]any{"requiredStatusChecks": []any{map[string]any{"context": "ci/legacy"}}}},
					map[string]any{"type": "DELETION", "parameters": nil},
				}, ""),
			}}})
		},
	}
}

func boolPtr(value bool) *bool { return &value }

func wantChecks(required func(bool) *bool) []bundle.Check {
	return []bundle.Check{
		{ID: "SC_1", Name: "ci/legacy", Kind: "status", Status: "success", Conclusion: "success", Required: required(true), URL: "https://ci.example.test/1", CompletedAt: when("04:05:00")},
		{ID: "check_run:12", Name: "build", Kind: "check_run", Status: "completed", Conclusion: "success", Required: required(true), URL: "https://github.com/acme/widgets/runs/12", StartedAt: when("04:00:00")},
		{ID: "check_run:13", Name: "test", Kind: "check_run", Status: "completed", Conclusion: "success", Required: required(false), URL: "https://github.com/acme/widgets/runs/13", StartedAt: when("04:00:00")},
		{ID: "check_run:21", Name: "lint", Kind: "check_run", Status: "in_progress", Required: required(false), URL: "https://github.com/acme/widgets/runs/21", StartedAt: when("04:00:00")},
	}
}

func TestFetchMapsEveryPageOntoTheBundle(t *testing.T) {
	fake := newFakeGitHub(t, fixtureRoutes(t))
	evidence, err := fake.client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}

	body := "Sizes for widgets.\n\n## Screens\n\n![Sizes](" + uploadURL + ")"
	wantPR := bundle.PullRequest{
		Number: 7, State: "open", Title: "Add widget sizes", Body: body,
		AuthorLogin: "octo-dev", AuthorAssociation: "MEMBER",
		BaseRef: "main", BaseSHA: baseSHA, HeadRef: "feature/sizes", HeadSHA: headSHA,
		RequestedReviewers: []string{"reviewer-one", "platform-team"}, OpenedAt: when("03:00:00"),
		Commits: []bundle.Commit{
			{SHA: commitOne, Subject: "Add sizes", CommittedAt: when("02:00:00"), Parents: []string{baseSHA}},
			{SHA: headSHA, Subject: "Fix size rounding", CommittedAt: when("04:00:00"), Parents: []string{commitOne}},
		},
	}
	if diff := cmp.Diff(wantPR, evidence.PullRequest); diff != "" {
		t.Errorf("pull request (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantChecks(boolPtr), evidence.Checks); diff != "" {
		t.Errorf("checks (-want +got):\n%s", diff)
	}

	resolved, open := true, false
	wantConversation := []bundle.ConversationItem{
		{ID: "101", Kind: "comment", AuthorLogin: "octo-dev", AuthorAssociation: "MEMBER", Body: "Ready for review.", CreatedAt: when("03:10:00")},
		{ID: "103", Kind: "comment", AuthorLogin: "octo-reviewer", AuthorAssociation: "MEMBER", Body: bundle.SummaryCommentMarker + " quoted by a person", CreatedAt: when("03:12:00")},
		{ID: "104", Kind: "comment", AuthorLogin: "copilot-reviewer", AuthorAssociation: "NONE", Bot: true, BotTool: "Copilot", Body: "Looks fine.", CreatedAt: when("03:13:00")},
		{ID: "105", Kind: "comment", AuthorLogin: "octo-dev", AuthorAssociation: "MEMBER", Bot: true, BotTool: "Claude", Body: "Generated with Claude Code", CreatedAt: when("03:14:00")},
		{ID: "201", Kind: "review", AuthorLogin: "octo-reviewer", AuthorAssociation: "MEMBER", Body: "Rounding is off.", State: "changes_requested", Commit: commitOne, CreatedAt: when("03:20:00")},
		{ID: "301", Kind: "review_comment", AuthorLogin: "octo-reviewer", AuthorAssociation: "MEMBER", Body: "Round half up.", Path: "size.go", Review: "201", CreatedAt: when("03:21:00"), Thread: "PRRT_1", ThreadRoot: true, Resolved: &resolved},
		{ID: "303", Kind: "review_comment", AuthorLogin: "cursor-bugbot", AuthorAssociation: "MEMBER", Bot: true, BotTool: "Cursor", Body: "Possible nil dereference.", Path: "old.go", Review: "203", CreatedAt: when("03:23:00"), Thread: "PRRT_2", ThreadRoot: true, Resolved: &open},
		{ID: "302", Kind: "review_comment", AuthorLogin: "octo-dev", AuthorAssociation: "MEMBER", Body: "Done.", Path: "size.go", Review: "204", CreatedAt: when("03:30:00"), Thread: "PRRT_1", Resolved: &resolved},
	}
	if diff := cmp.Diff(wantConversation, evidence.Conversation); diff != "" {
		t.Errorf("conversation (-want +got):\n%s", diff)
	}
	line := 12
	wantThreads := []bundle.Thread{
		{ID: "PRRT_1", Path: "size.go", Line: &line, Resolved: true, URL: "https://github.com/acme/widgets/pull/7#discussion_r301"},
		{ID: "PRRT_2", Path: "old.go", URL: "https://github.com/acme/widgets/pull/7#discussion_r303"},
	}
	if diff := cmp.Diff(wantThreads, evidence.Threads); diff != "" {
		t.Errorf("threads (-want +got):\n%s", diff)
	}

	wantImages := []bundle.RenderedImage{{ID: bundle.AttachmentID(uploadURL), SourceURL: uploadURL, Alt: "Sizes", FetchURL: signedURL}}
	if diff := cmp.Diff(wantImages, evidence.Images); diff != "" {
		t.Errorf("images (-want +got):\n%s", diff)
	}
	wantSources := bundle.Sources{
		SourcePullRequest: true, SourceRequestedReviewers: true, SourceCommits: true, SourceComments: true, SourceReviews: true,
		SourceReviewThreads: true, SourceChecks: true, SourceRequiredChecks: true,
	}
	if diff := cmp.Diff(wantSources, evidence.Sources); diff != "" || len(evidence.Unavailable) != 0 {
		t.Errorf("sources (-want +got):\n%s\nunavailable: %v", diff, evidence.Unavailable)
	}
	if evidence.Repository != "Acme/widgets" || evidence.Host != "github.com" {
		t.Errorf("repository = %q on %q, want GitHub's spelling on github.com", evidence.Repository, evidence.Host)
	}
	if hosts := fake.tokenHosts; len(hosts) != 1 || hosts[0] != "github.com" {
		t.Errorf("tokens asked for %v, want github.com's once", hosts)
	}

	var b bundle.Bundle
	evidence.Apply(&b)
	if b.Repository != "Acme/widgets" || b.PullRequest.HeadSHA != headSHA || len(b.Checks) != 4 || !b.Sources[SourceReviewThreads] {
		t.Errorf("Apply copied %+v", b)
	}
}

func TestFetchSendsTheTokenAndGitHubFieldNames(t *testing.T) {
	fake := newFakeGitHub(t, fixtureRoutes(t))
	if _, err := fake.client(nil).Fetch(context.Background(), acme, 7, FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, request := range fake.recorded() {
		if request.Authorization != "token test-token" || request.URL != "https://api.github.com/graphql" {
			t.Fatalf("%s went to %s with Authorization %q", request.OperationName, request.URL, request.Authorization)
		}
	}
	queries := joinQueries(fake.recorded())
	for _, field := range []string{
		"bodyHTML", "baseRefOid", "headRefOid", "fullDatabaseId", "authorAssociation", "__typename",
		"reviewRequests(first: 100, after: $cursor)", "state", "isResolved", "isOutdated", "diffSide", "reviewThreads(first: 50, after: $cursor)",
		"object(oid: $oid)", "$oid:GitObjectID!", "checkType: ALL", "permalink", "targetUrl",
		"refUpdateRule", "requiredStatusCheckContexts", "... on RequiredStatusChecksParameters",
	} {
		if !strings.Contains(queries, field) {
			t.Errorf("no query asks for %q", field)
		}
	}
}

func TestFetchLeavesRequiredUnknownWhenProtectionIsUnreadable(t *testing.T) {
	for name, response := range map[string]fakeResponse{
		"forbidden": graphQLError(map[string]any{"repository": map[string]any{"ref": nil}}, "FORBIDDEN", "Resource not accessible by personal access token"),
		"no branch": data(map[string]any{"repository": map[string]any{"ref": nil}}),
		"more rules than a page": data(map[string]any{"repository": map[string]any{"ref": map[string]any{
			"refUpdateRule": nil, "rules": page([]any{}, "rules-1"),
		}}}),
	} {
		t.Run(name, func(t *testing.T) {
			routes := fixtureRoutes(t)
			routes["RequiredChecks"] = func(gqlRequest, int) fakeResponse { return response }
			evidence, err := newFakeGitHub(t, routes).client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
			if err != nil {
				t.Fatal(err)
			}
			unknown := func(bool) *bool { return nil }
			if diff := cmp.Diff(wantChecks(unknown), evidence.Checks); diff != "" {
				t.Errorf("checks (-want +got):\n%s", diff)
			}
			if evidence.Sources[SourceRequiredChecks] || evidence.Unavailable[SourceRequiredChecks] == nil || !evidence.Sources[SourceChecks] {
				t.Errorf("sources = %v, unavailable = %v", evidence.Sources, evidence.Unavailable)
			}
		})
	}
}

func TestFetchMarksNothingRequiredWithoutProtection(t *testing.T) {
	routes := fixtureRoutes(t)
	routes["RequiredChecks"] = func(gqlRequest, int) fakeResponse {
		return data(map[string]any{"repository": map[string]any{"ref": map[string]any{"refUpdateRule": nil, "rules": page([]any{}, "")}}})
	}
	evidence, err := newFakeGitHub(t, routes).client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	notRequired := func(bool) *bool { return boolPtr(false) }
	if diff := cmp.Diff(wantChecks(notRequired), evidence.Checks); diff != "" {
		t.Errorf("checks (-want +got):\n%s", diff)
	}
}

func TestFetchRecordsAPartGitHubRefuses(t *testing.T) {
	routes := fixtureRoutes(t)
	routes["HeadChecks"] = func(gqlRequest, int) fakeResponse {
		return graphQLError(map[string]any{"repository": map[string]any{"object": nil}}, "FORBIDDEN", "Resource not accessible by personal access token")
	}
	routes["PullRequestReviewThreads"] = func(request gqlRequest, call int) fakeResponse {
		if request.cursor() == "threads-1" {
			return fakeResponse{status: http.StatusInternalServerError, body: map[string]any{"message": "boom"}}
		}
		return fixtureRoutes(t)["PullRequestReviewThreads"](request, call)
	}
	evidence, err := newFakeGitHub(t, routes).client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Checks != nil || evidence.Sources[SourceChecks] ||
		!strings.Contains(evidence.Unavailable[SourceChecks].Error(), "Resource not accessible") {
		t.Errorf("checks = %v, sources = %v, unavailable = %v", evidence.Checks, evidence.Sources, evidence.Unavailable)
	}
	// A thread page failing after the first leaves no partial thread list,
	// and no review comment from the pages that did arrive.
	if len(evidence.Threads) != 0 || evidence.Sources[SourceReviewThreads] || evidence.Unavailable[SourceReviewThreads] == nil ||
		evidence.Unavailable[UnavailableReviewComments] == nil {
		t.Errorf("threads = %v, sources = %v", evidence.Threads, evidence.Sources)
	}
	for _, item := range evidence.Conversation {
		if item.Kind == "review_comment" {
			t.Errorf("review comment %s kept without its threads", item.ID)
		}
	}
	if !evidence.Sources[SourceComments] || !evidence.Sources[SourceReviews] || !evidence.Sources[SourceCommits] || len(evidence.Conversation) != 5 {
		t.Errorf("the other parts should be intact: sources = %v, conversation = %d", evidence.Sources, len(evidence.Conversation))
	}
}

func TestFetchBacksOffRateLimitsAndUnavailability(t *testing.T) {
	routes := fixtureRoutes(t)
	reviews := routes["PullRequestReviews"]
	routes["PullRequestReviews"] = func(request gqlRequest, call int) fakeResponse {
		switch call {
		case 1:
			return fakeResponse{status: http.StatusForbidden, header: http.Header{"Retry-After": {"30"}},
				body: map[string]any{"message": "You have exceeded a secondary rate limit."}}
		case 2:
			return fakeResponse{status: http.StatusTooManyRequests, body: map[string]any{"message": "slow down"}}
		case 3:
			return fakeResponse{status: http.StatusOK, body: map[string]any{"errors": []any{map[string]any{"type": "RATE_LIMITED", "message": "API rate limit exceeded"}}}}
		case 4:
			return fakeResponse{status: http.StatusBadGateway, body: map[string]any{"message": "bad gateway"}}
		}
		return reviews(request, call)
	}
	fake := newFakeGitHub(t, routes)
	var waits []time.Duration
	evidence, err := fake.client(&waits).Fetch(context.Background(), acme, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(waits) != 4 || waits[0] <= 25*time.Second || waits[0] > 30*time.Second ||
		waits[1] != 20*time.Second || waits[2] != 40*time.Second || waits[3] != 8*time.Second {
		t.Errorf("waits = %v, want Retry-After's 30s, then doubling backoff", waits)
	}
	if fake.callCount("PullRequestReviews") != 5 || !evidence.Sources[SourceReviews] {
		t.Errorf("reviews read %d times, sources = %v", fake.callCount("PullRequestReviews"), evidence.Sources)
	}
}

func TestFetchFailsOnARateLimitItWillNotWaitFor(t *testing.T) {
	routes := fixtureRoutes(t)
	routes["PullRequestComments"] = func(gqlRequest, int) fakeResponse {
		return fakeResponse{status: http.StatusForbidden, header: http.Header{"X-Ratelimit-Remaining": {"0"}, "Retry-After": {"3600"}}}
	}
	fake := newFakeGitHub(t, routes)
	var waits []time.Duration
	_, err := fake.client(&waits).Fetch(context.Background(), acme, 7, FetchOptions{})
	var limited *RateLimitedError
	if !errors.As(err, &limited) || time.Until(limited.ResumeAt) < 59*time.Minute {
		t.Fatalf("err = %v, want a rate limit resuming in an hour", err)
	}
	if len(waits) != 0 || fake.callCount("PullRequestComments") != 1 || fake.callCount("HeadChecks") != 0 {
		t.Errorf("waits = %v, comment calls = %d, check calls = %d", waits, fake.callCount("PullRequestComments"), fake.callCount("HeadChecks"))
	}
}

func TestFetchStartsOverWhenTheHeadMoves(t *testing.T) {
	routes := fixtureRoutes(t)
	comments := routes["PullRequestComments"]
	routes["PullRequestComments"] = func(request gqlRequest, call int) fakeResponse {
		if call == 2 {
			return prConnection(movedSHA, "comments", page([]any{}, ""))
		}
		return comments(request, call)
	}
	fake := newFakeGitHub(t, routes)
	evidence, err := fake.client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if fake.callCount("PullRequest") != 2 || evidence.PullRequest.HeadSHA != headSHA || !evidence.Sources[SourceComments] {
		t.Errorf("header reads = %d, head = %s, sources = %v", fake.callCount("PullRequest"), evidence.PullRequest.HeadSHA, evidence.Sources)
	}

	routes["PullRequestCommits"] = func(gqlRequest, int) fakeResponse {
		return prConnection(movedSHA, "commits", page([]any{}, ""))
	}
	fake = newFakeGitHub(t, routes)
	if _, err := fake.client(nil).Fetch(context.Background(), acme, 7, FetchOptions{}); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("err = %v, want ErrHeadMoved", err)
	}
	if fake.callCount("PullRequest") != fetchAttempts {
		t.Errorf("header reads = %d, want %d", fake.callCount("PullRequest"), fetchAttempts)
	}
}

func TestFetchReportsAMissingPullRequest(t *testing.T) {
	fake := newFakeGitHub(t, map[string]route{"PullRequest": func(gqlRequest, int) fakeResponse {
		return graphQLError(map[string]any{"repository": map[string]any{"nameWithOwner": "acme/widgets", "pullRequest": nil}},
			"NOT_FOUND", "Could not resolve to a PullRequest with the number of 7.")
	}})
	if _, err := fake.client(nil).Fetch(context.Background(), acme, 7, FetchOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFetchFlagsRegisteredBotReports(t *testing.T) {
	routes := fixtureRoutes(t)
	evidence, err := newFakeGitHub(t, routes).client(nil).Fetch(context.Background(), acme, 7, FetchOptions{BotReportMarkers: []string{"Looks fine"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range evidence.Conversation {
		if item.BotReport != (item.ID == "104") {
			t.Errorf("item %s BotReport = %v", item.ID, item.BotReport)
		}
	}
}

func TestFetchLeavesOutDraftReviewComments(t *testing.T) {
	evidence, err := newFakeGitHub(t, fixtureRoutes(t)).client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range evidence.Conversation {
		if item.ID == "304" || item.ID == "305" {
			t.Errorf("draft comment %s entered the conversation", item.ID)
		}
	}
	for _, thread := range evidence.Threads {
		if thread.ID == "PRRT_3" {
			t.Error("a thread holding only drafts entered the threads")
		}
	}
	if len(evidence.Threads) != 2 {
		t.Errorf("threads = %+v", evidence.Threads)
	}
}

func TestFetchKeepsGoingWhenTeamReviewersNeedAScope(t *testing.T) {
	routes := fixtureRoutes(t)
	routes["PullRequestReviewRequests"] = func(gqlRequest, int) fakeResponse {
		return graphQLError(map[string]any{"repository": map[string]any{"pullRequest": nil}}, "INSUFFICIENT_SCOPES",
			"Your token has not been granted the required scopes to execute this query. The 'slug' field requires one of the following scopes: ['read:org'].")
	}
	evidence, err := newFakeGitHub(t, routes).client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.PullRequest.RequestedReviewers == nil || len(evidence.PullRequest.RequestedReviewers) != 0 ||
		evidence.Sources[SourceRequestedReviewers] || evidence.Unavailable[SourceRequestedReviewers] == nil {
		t.Errorf("reviewers = %#v, sources = %v, unavailable = %v",
			evidence.PullRequest.RequestedReviewers, evidence.Sources, evidence.Unavailable)
	}
	if evidence.PullRequest.Title == "" || !evidence.Sources[SourceCommits] || !evidence.Sources[SourceChecks] || len(evidence.Conversation) == 0 {
		t.Errorf("the rest of the pull request should be read: sources = %v", evidence.Sources)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var mu sync.Mutex
	var elsewhere []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		elsewhere = append(elsewhere, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(other.Close)
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusMovedPermanently} {
		fake := newFakeGitHub(t, map[string]route{"PullRequest": func(gqlRequest, int) fakeResponse {
			return fakeResponse{status: status, header: http.Header{"Location": {other.URL + "/graphql"}}, body: map[string]any{}}
		}})
		if _, err := fake.client(nil).Fetch(context.Background(), acme, 7, FetchOptions{}); err == nil {
			t.Errorf("%d: the fetch followed the redirect", status)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(elsewhere) != 0 {
		t.Errorf("the other host received %d requests, with Authorization %q", len(elsewhere), elsewhere)
	}
}

func TestRateLimitedErrorHasNoResumeTimeGitHubDidNotGive(t *testing.T) {
	routes := fixtureRoutes(t)
	routes["PullRequest"] = func(gqlRequest, int) fakeResponse {
		return fakeResponse{status: http.StatusTooManyRequests, body: map[string]any{"message": "slow down"}}
	}
	fake := newFakeGitHub(t, routes)
	var waits []time.Duration
	_, err := fake.client(&waits).Fetch(context.Background(), acme, 7, FetchOptions{})
	var limited *RateLimitedError
	if !errors.As(err, &limited) || !limited.ResumeAt.IsZero() || !strings.Contains(limited.Error(), "try again later") {
		t.Fatalf("err = %v, want a rate limit without a resume time", err)
	}
	if len(waits) != 4 || fake.callCount("PullRequest") != 5 {
		t.Errorf("waits = %v after %d calls; want four backoffs and five calls", waits, fake.callCount("PullRequest"))
	}
}

func TestFetchStopsWaitingWhenCancelled(t *testing.T) {
	routes := fixtureRoutes(t)
	routes["PullRequestComments"] = func(gqlRequest, int) fakeResponse {
		return fakeResponse{status: http.StatusServiceUnavailable, body: map[string]any{"message": "unavailable"}}
	}
	fake := newFakeGitHub(t, routes)
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	c := New(fake.testTokens, Options{Transport: fake.transport()})
	c.retryBackoff = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for fake.callCount("PullRequestComments") == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	started := time.Now()
	_, err := c.Fetch(ctx, acme, 7, FetchOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("Fetch took %v to notice the cancellation", elapsed)
	}
}

func TestFetchReadsAnEnterpriseHost(t *testing.T) {
	fake := newFakeGitHub(t, fixtureRoutes(t))
	client := fake.client(nil)
	t.Setenv("GH_HOST", "ghe.example.com")
	ghe := Repository{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	evidence, err := client.Fetch(context.Background(), ghe, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Host != "ghe.example.com" || len(evidence.Unavailable) != 0 {
		t.Errorf("host = %q, unavailable = %v", evidence.Host, evidence.Unavailable)
	}
	for _, request := range fake.recorded() {
		if request.URL != "https://ghe.example.com/api/graphql" || request.Authorization != "token test-token" {
			t.Fatalf("%s went to %s with Authorization %q", request.OperationName, request.URL, request.Authorization)
		}
	}
	if hosts := fake.tokenHosts; len(hosts) != 1 || hosts[0] != "ghe.example.com" {
		t.Errorf("tokens asked for %v, want the Enterprise host's once", hosts)
	}
}

// TestGHDebugDoesNotLogRequests checks GH_DEBUG, which turns on the GitHub
// CLI's request log, cannot make baum print requests and their
// Authorization header.
func TestGHDebugDoesNotLogRequests(t *testing.T) {
	t.Setenv("GH_DEBUG", "api")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan []byte)
	go func() {
		logged, _ := io.ReadAll(reader)
		read <- logged
	}()
	stderr := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = stderr }()
	_, err = newFakeGitHub(t, fixtureRoutes(t)).client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
	os.Stderr = stderr
	_ = writer.Close()
	logged := <-read
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 0 {
		t.Errorf("GH_DEBUG made the client log %d bytes: %.200q", len(logged), logged)
	}
}

// TestRequestsCarryOnlyBaumsHeaders checks the client sends no time zone,
// API version or preview media types, only its own identity and the token.
func TestRequestsCarryOnlyBaumsHeaders(t *testing.T) {
	fake := newFakeGitHub(t, fixtureRoutes(t))
	if _, err := fake.client(nil).Fetch(context.Background(), acme, 7, FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, request := range fake.recorded() {
		header := request.Header
		if header.Get("User-Agent") != "baum" || header.Get("Time-Zone") != "" ||
			header.Get("X-GitHub-Api-Version") != "" || strings.Contains(header.Get("Accept"), "preview") {
			t.Fatalf("%s sent headers %v", request.OperationName, header)
		}
	}
}

// TestResponseTextIsNotRewritten checks control characters in GitHub's
// answer reach the bundle as GitHub sent them, as the hosted service stores
// them, so both hosts digest the same text.
func TestResponseTextIsNotRewritten(t *testing.T) {
	const body = "Colour \x1b[31mred\x1b[0m, next line\u0085, bell\a."
	routes := fixtureRoutes(t)
	routes["PullRequestComments"] = func(gqlRequest, int) fakeResponse {
		return prConnection(headSHA, "comments", page([]any{
			map[string]any{"fullDatabaseId": "101", "authorAssociation": "MEMBER", "author": user("octo-dev"), "body": body, "createdAt": at("03:10:00")},
		}, ""))
	}
	evidence, err := newFakeGitHub(t, routes).client(nil).Fetch(context.Background(), acme, 7, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range evidence.Conversation {
		if item.ID == "101" && item.Body != body {
			t.Fatalf("body = %q, want %q", item.Body, body)
		}
	}
}

func TestFetchChecksReviewedHeadWhileMetadataStaysCurrent(t *testing.T) {
	routes := fixtureRoutes(t)
	routes["HeadChecks"] = func(request gqlRequest, _ int) fakeResponse {
		if request.Variables["oid"] != movedSHA {
			t.Errorf("checks oid = %v", request.Variables["oid"])
		}
		return data(map[string]any{"repository": map[string]any{"object": map[string]any{"oid": movedSHA, "checkSuites": page([]any{}, ""), "status": nil}}})
	}
	fake := newFakeGitHub(t, routes)
	evidence, err := fake.client(nil).Fetch(context.Background(), acme, 7, FetchOptions{ChecksHead: movedSHA})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.PullRequest.HeadSHA != headSHA || !evidence.Sources[SourceChecks] || !evidence.Sources[SourceCommits] || !evidence.Sources[SourceReviewThreads] {
		t.Fatalf("metadata reader head changed: %+v", evidence)
	}
}
