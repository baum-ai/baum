package github

import (
	"context"
	"testing"
)

func TestAllThreadsKeepsResolvedThreads(t *testing.T) {
	reply := `{"data":{"repository":{"pullRequest":{"headRefOid":"abc","reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[
		{"id":"RT_1","path":"a.go","line":1,"isResolved":false,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"open","author":{"__typename":"User","login":"alice"}}]}},
		{"id":"RT_2","path":"b.go","line":2,"isResolved":true,"comments":{"totalCount":1,"nodes":[{"state":"SUBMITTED","body":"done","author":{"__typename":"User","login":"bob"}}]}}]}}}}}`
	var sent []string
	client := threadsClient(func(string, map[string]any) string { return reply }, &sent)
	repo := Repository{Host: "github.com", Owner: "acme", Name: "app"}
	all, err := client.AllThreads(context.Background(), repo, 7)
	if err != nil || len(all.All) != 2 || len(all.Unresolved) != 0 || all.All[0].Resolved || !all.All[1].Resolved {
		t.Fatalf("all %+v %v", all, err)
	}
	open, err := client.UnresolvedThreads(context.Background(), repo, 7)
	if err != nil || len(open.Unresolved) != 1 || open.Unresolved[0].ID != "RT_1" || len(open.All) != 0 {
		t.Fatalf("unresolved %+v %v", open, err)
	}
}
