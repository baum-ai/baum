package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/review"
)

// Group asks Run for a review group's combined review instead of one pull
// request's: Request names the repository and the limits, and its Number is
// ignored. See internal/reviewgroups.
type Group struct {
	// Name is the group's name, the document's stack title.
	Name string
	// Members are the group's pull requests in group order.
	Members []GroupMember
	// MemberReviews is written to the run directory as members.json, the
	// members' own reviews in brief for the model to read.
	MemberReviews []byte
}

// GroupMember is one pull request of a group.
type GroupMember struct {
	Number int
	// Head, when not empty, is the head the run was confirmed for: a member
	// whose head moved fails the run before any model call.
	Head string
	// ReviewVersion is the member's saved review version the run reads,
	// recorded in the document's stack; at least 1.
	ReviewVersion int
}

// MaxGroupMembers bounds a group; internal/reviewgroups enforces it on
// definitions.
const MaxGroupMembers = 8

// assembleGroup assembles every member on its own, each into its own
// checkout archive, merges their bundles, unpacks each member's checkout at
// members/<number>/ in the run directory, writes members.json there, and
// returns the merged assembly with the decorator that fills the document's
// stack and its sections' members.
func assembleGroup(ctx context.Context, o Options, archives, runDir string, fail func(Category, error) error, interrupted func(error) error) (assemble.Result, func([]byte) []byte, error) {
	group := o.Group
	if len(group.Members) == 0 || len(group.Members) > MaxGroupMembers {
		return assemble.Result{}, nil, fail(CategoryInput, fmt.Errorf("a review group has 1 to %d pull requests, not %d", MaxGroupMembers, len(group.Members)))
	}
	request := o.Request
	limits := request.Limits
	// The members share the checkout budget, so a group's checkouts take no
	// more room than one pull request's.
	share := limits
	share.Checkout.MaxTotalBytes /= int64(len(group.Members))
	share.Checkout.MaxTotalFiles /= len(group.Members)
	members := make([]assemble.GroupMember, 0, len(group.Members))
	paths := make([]string, 0, len(group.Members))
	for _, member := range group.Members {
		if member.Number <= 0 || member.ReviewVersion <= 0 {
			return assemble.Result{}, nil, fail(CategoryInput, fmt.Errorf("pull request #%d of the group has no saved review", member.Number))
		}
		path := filepath.Join(archives, "member-"+strconv.Itoa(member.Number)+".tar.gz")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return assemble.Result{}, nil, err
		}
		assembled, err := o.Assembler.Assemble(ctx, assemble.Request{Repository: request.Repository, Number: member.Number, Limits: share, Checkout: file})
		closeErr := file.Close()
		if err != nil {
			err = interrupted(err)
			if CategoryOf(err) != "" {
				return assemble.Result{}, nil, err
			}
			if errors.Is(err, github.ErrNotFound) {
				err = fmt.Errorf("pull request %s#%d was not found: it does not exist, or your token cannot see it: %w", request.Repository, member.Number, err)
			}
			return assemble.Result{}, nil, fail(CategoryInput, fmt.Errorf("pull request #%d: %w", member.Number, err))
		}
		if closeErr != nil {
			return assemble.Result{}, nil, closeErr
		}
		if member.Head != "" && assembled.Head != member.Head {
			return assemble.Result{}, nil, fail(CategoryInput, fmt.Errorf("pull request #%d head changed; start it again", member.Number))
		}
		members = append(members, assemble.GroupMember{Number: member.Number, Result: assembled})
		paths = append(paths, path)
	}
	merged, err := assemble.MergeGroup(ctx, group.Name, members, request.Previous, limits.Caps, o.Redactor)
	if err != nil {
		if err = interrupted(err); CategoryOf(err) != "" {
			return assemble.Result{}, nil, err
		}
		return assemble.Result{}, nil, fail(CategoryInput, err)
	}
	if err := review.FitPreviousHunkAnchors(&merged.Bundle); err != nil {
		return assemble.Result{}, nil, fail(CategoryInput, fmt.Errorf("previous group review: %w", err))
	}
	for i, member := range group.Members {
		dir := filepath.Join(runDir, "members", strconv.Itoa(member.Number))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return assemble.Result{}, nil, err
		}
		if err := bundle.UnpackCheckout(ctx, paths[i], dir, share.Checkout); err != nil {
			if err = interrupted(err); CategoryOf(err) != "" {
				return assemble.Result{}, nil, err
			}
			return assemble.Result{}, nil, fail(CategoryInput, fmt.Errorf("unpacking the checkout of #%d: %w", member.Number, err))
		}
	}
	reviews := group.MemberReviews
	if len(reviews) == 0 {
		reviews = []byte("[]\n")
	}
	if err := os.WriteFile(filepath.Join(runDir, "members.json"), reviews, 0o400); err != nil {
		return assemble.Result{}, nil, err
	}
	stack := documentStack{Title: merged.Bundle.PullRequest.Title}
	for i, member := range group.Members {
		stack.Members = append(stack.Members, stackMember{Number: member.Number, HeadSHA: members[i].Result.Head, ReviewVersion: member.ReviewVersion})
	}
	return merged.Result, groupDecorator(stack, merged.HunkMembers), nil
}

type documentStack struct {
	Title   string        `json:"title"`
	Members []stackMember `json:"members"`
}

type stackMember struct {
	Number        int    `json:"number"`
	HeadSHA       string `json:"head_sha"`
	ReviewVersion int    `json:"review_version"`
}

// groupDecorator returns the agent.Run decorator of a group review: it sets
// the document's stack and gives each section the members whose hunks it
// lists, in member order, replacing whatever the model wrote there; a
// section listing no member's hunk names none.
func groupDecorator(stack documentStack, hunkMembers map[string][]int) func([]byte) []byte {
	return func(document []byte) []byte {
		decoder := json.NewDecoder(bytes.NewReader(document))
		decoder.UseNumber()
		var doc map[string]any
		if decoder.Decode(&doc) != nil || doc == nil {
			return document
		}
		doc["stack"] = stack
		sections, _ := doc["sections"].([]any)
		for _, raw := range sections {
			section, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			covered := map[int]bool{}
			hunks, _ := section["hunks"].([]any)
			for _, hunk := range hunks {
				if id, ok := hunk.(string); ok {
					for _, number := range hunkMembers[id] {
						covered[number] = true
					}
				}
			}
			var numbers []int
			for _, member := range stack.Members {
				if covered[member.Number] {
					numbers = append(numbers, member.Number)
				}
			}
			if len(numbers) > 0 {
				section["pull_requests"] = numbers
			} else {
				delete(section, "pull_requests")
			}
		}
		encoded, err := json.Marshal(doc)
		if err != nil {
			return document
		}
		return encoded
	}
}
