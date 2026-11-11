package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// groupTools are the tools of the review group page. A group belongs to one
// repository, so these are repository-scoped: an action never reads a group
// of another.
func (s *Server) groupTools() []Tool {
	schema := json.RawMessage(`{"type":"object","properties":{` +
		`"repository":{"type":"string","description":"The group's repository as owner/name. Defaults to the page's, or for an action its own."},` +
		`"id":{"type":"string","description":"The group's id. Defaults to the open group page's, or for an action the one group its pull request is in."}},"additionalProperties":false}`)
	return []Tool{{
		Name:        "group",
		Description: "A review group as the app's group page shows it: its name, the pull requests that are its members, whether baum made it for a stack, the members that landed, and whether its review is outdated.",
		InputSchema: schema,
		Scope:       ScopeRepository,
		Reads:       []string{"/api/groups"},
		Call:        callGroup,
	}, {
		Name:        "group_versions",
		Description: "The saved combined reviews of a review group.",
		InputSchema: schema,
		Scope:       ScopeRepository,
		Reads:       []string{"/api/groups"},
		Call:        callGroupVersions,
	}}
}

// groupOf finds the group the call names and checks that it belongs to the
// call's repository.
func groupOf(ctx context.Context, c CallContext, args json.RawMessage) (id string, group json.RawMessage, err error) {
	var a struct {
		Repository string `json:"repository"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", nil, err
	}
	repository, _, err := c.Target(a.Repository, 0)
	if err != nil {
		return "", nil, err
	}
	id = a.ID
	if id == "" && c.Consumer.Kind != Action && c.HasPage {
		id = c.Page.Group
	}
	if id == "" && c.Consumer.Kind == Action {
		// An action's group is the one its pull request is in, when it is in
		// exactly one.
		groups, err := c.groupsOf(ctx, repository, c.Consumer.PullRequest)
		if err != nil {
			return "", nil, err
		}
		switch len(groups) {
		case 1:
			id = groups[0].ID
		case 0:
			return "", nil, errors.New("name a group: the pull request is in none")
		default:
			ids := make([]string, len(groups))
			for i, g := range groups {
				ids[i] = g.ID
			}
			return "", nil, fmt.Errorf("name a group: the pull request is in several (%s)", strings.Join(ids, ", "))
		}
	}
	if id == "" {
		return "", nil, errors.New("name a group: no group page is open")
	}
	if err := segment("group", id); err != nil {
		return "", nil, err
	}
	group, missing, err := c.get(ctx, "/api/groups/"+url.PathEscape(id), nil)
	if err != nil {
		return "", nil, err
	}
	var v struct {
		Repository string `json:"repository"`
	}
	if missing || json.Unmarshal(group, &v) != nil || !strings.EqualFold(v.Repository, repository) {
		return "", nil, fmt.Errorf("group %s is not a group of %s", id, repository)
	}
	return id, group, nil
}

func callGroup(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	_, group, err := groupOf(ctx, c, args)
	return string(group), err
}

func callGroupVersions(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	id, _, err := groupOf(ctx, c, args)
	if err != nil {
		return "", err
	}
	body, missing, err := c.get(ctx, "/api/groups/"+url.PathEscape(id)+"/versions", nil)
	if err != nil {
		return "", err
	}
	if missing {
		return "", fmt.Errorf("group %s has no versions route", id)
	}
	return string(body), nil
}
