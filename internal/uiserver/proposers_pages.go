package uiserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/prfilter"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/uistate"
)

// promptFailure is what the prompt routes answer for an error of the prompt
// store (writePromptFailure), as an error.
func promptFailure(err error) error {
	switch {
	case errors.Is(err, repos.ErrNotPicked):
		return errors.New("repository is not picked")
	case errors.Is(err, repos.ErrInvalidPrompt):
		return errors.New(strings.TrimPrefix(err.Error(), repos.ErrInvalidPrompt.Error()+": "))
	}
	return err
}

// promptTarget is one prompt of a scope ("" is global), saved as
// PUT .../prompts/{id} saves it. A text is validated as the route does
// (prompt.Validate). An agent may not take a safety rule out of the prompt
// in use: a text, or a reset to the text inherited, that leaves out a rule
// the prompt in use has is refused. The person can still do that in the
// prompt's editor, which warns of it.
func (p *Proposers) promptTarget(scope, id string) (*target, error) {
	if _, ok := prompt.Lookup(prompt.ID(id)); !ok {
		return nil, errors.New("unknown prompt")
	}
	registry := p.API.Registry
	decode := func(after json.RawMessage) (*string, error) {
		var in agentcontext.PromptChange
		if err := decodeAfter(after, &in); err != nil {
			return nil, err
		}
		if !afterKeys(after)["text"] {
			return nil, errors.New("text is required: the new text, or null to reset the prompt")
		}
		if in.Text != nil && *in.Text == "" {
			// The route resets the prompt for "" as for null.
			in.Text = nil
		}
		return in.Text, nil
	}
	return &target{
		read: func(ctx context.Context) (any, any, error) {
			state, err := registry.PromptState(ctx, scope, prompt.ID(id))
			if err != nil {
				return nil, nil, promptFailure(err)
			}
			value := agentcontext.PromptChange{}
			if state.Own != nil {
				value.Text = &state.Own.Text
			}
			return value, state, nil
		},
		validate: func(ctx context.Context, after json.RawMessage) error {
			text, err := decode(after)
			if err != nil {
				return err
			}
			entries, err := registry.PromptEntries(ctx, scope)
			if err != nil {
				return promptFailure(err)
			}
			i := slices.IndexFunc(entries, func(e repos.PromptEntry) bool { return e.ID == id })
			if i < 0 {
				return errors.New("unknown prompt")
			}
			entry := entries[i]
			// What runs after the save: the text, or after a reset the
			// built-in text, or for a repository the text it inherits.
			next := entry.DefaultText
			if text != nil {
				if err := prompt.Validate(prompt.ID(id), *text); err != nil {
					return err
				}
				next = *text
			} else {
				if entry.CustomText == nil {
					return errors.New("the prompt is not customised here: there is nothing to reset")
				}
				if entry.InheritedText != nil {
					next = *entry.InheritedText
				}
			}
			var removed []string
			for _, missing := range prompt.MissingSafetyRules(prompt.ID(id), next) {
				if !slices.Contains(entry.MissingRules, missing) {
					for _, rule := range entry.SafetyRules {
						if rule.ID == missing {
							removed = append(removed, rule.Label)
						}
					}
				}
			}
			if len(removed) > 0 {
				return fmt.Errorf("the change would take a safety rule out of the prompt in use (%s): an agent may not propose that", strings.Join(removed, "; "))
			}
			return nil
		},
		apply: func(ctx context.Context, after json.RawMessage, unchanged func(any) error, _ agentcontext.SaveInput) error {
			text, err := decode(after)
			if err != nil {
				return err
			}
			err = registry.PutPromptOverrideIf(ctx, scope, prompt.ID(id), text, func(state repos.PromptState) error { return unchanged(state) })
			if errors.Is(err, agentcontext.ErrStale) {
				return err
			}
			if err != nil {
				return promptFailure(err)
			}
			// The app server follows a write to a repository's routes, its
			// prompts' included, with these.
			if scope != repos.GlobalPrompts {
				p.repositoryChanged()
			}
			return nil
		},
	}, nil
}

// repositoryChanged is what the app server does after a write to the
// repository routes (LocalAPI.Handler's mutate).
func (p *Proposers) repositoryChanged() {
	if p.API.Changed != nil {
		p.API.Changed()
	}
	if p.API.DecisionsChanged != nil {
		p.API.DecisionsChanged()
	}
}

// repositoryTarget is a target of a repository's page: its Reviews section's
// settings, or a prompt of its Prompts section. It must be the repository
// the page shows.
func (p *Proposers) repositoryTarget(page agentcontext.PageContext, name string) (*target, error) {
	repository, what, ok := agentcontext.ParseRepositoryTarget(name)
	if !ok {
		return nil, fmt.Errorf("a repository's page has no setting %q that takes proposals", name)
	}
	if page.Repository == "" {
		return nil, errors.New("the person has the Repositories page open without a repository: a proposal is for a repository's page they see")
	}
	if !strings.EqualFold(repository, page.Repository) {
		return nil, fmt.Errorf("the person has %s's page open: a proposal is for that repository", page.Repository)
	}
	repository = page.Repository
	switch {
	case what == agentcontext.TargetRepositorySettings:
		if err := checkSection(page, "reviews", "reviews", "the repository's page"); err != nil {
			return nil, err
		}
		return p.repositorySettingsTarget(repository), nil
	case strings.HasPrefix(what, agentcontext.TargetPrompt) && len(what) > len(agentcontext.TargetPrompt):
		if err := checkSection(page, "prompts", "reviews", "the repository's page"); err != nil {
			return nil, err
		}
		return p.promptTarget(repository, strings.TrimPrefix(what, agentcontext.TargetPrompt))
	}
	return nil, fmt.Errorf("a repository's page has no setting %q that takes proposals", what)
}

// repositorySettingsTarget is a repository's shared settings, saved as
// PUT /api/repositories/{owner}/{name}/settings saves them: the whole
// settings object, of which an After names the fields that change.
func (p *Proposers) repositorySettingsTarget(repository string) *target {
	registry := p.API.Registry
	decode := func(after json.RawMessage) (map[string]*bool, error) {
		var in map[string]*bool
		if err := decodeAfter(after, &in); err != nil {
			return nil, err
		}
		if len(in) == 0 {
			return nil, errors.New("propose automatic, decisions or both")
		}
		for key := range in {
			if key != "automatic" && key != "decisions" {
				return nil, errors.New("the proposal is not a change this page takes")
			}
		}
		return in, nil
	}
	return &target{
		read: func(ctx context.Context) (any, any, error) {
			settings, picked, err := registry.RepositorySettings(ctx, repository)
			if err != nil {
				return nil, nil, errors.New("the repository's settings could not be read")
			}
			if !picked {
				return nil, nil, fmt.Errorf("%s is not picked in baum", repository)
			}
			return settings, settings, nil
		},
		validate: func(_ context.Context, after json.RawMessage) error { _, err := decode(after); return err },
		apply: func(ctx context.Context, after json.RawMessage, unchanged func(any) error, _ agentcontext.SaveInput) error {
			in, err := decode(after)
			if err != nil {
				return err
			}
			_, err = registry.UpdateRepositoryIf(ctx, repository, func(current repos.Settings) (repos.Settings, error) {
				if err := unchanged(current); err != nil {
					return current, err
				}
				next := current
				if v, ok := in["automatic"]; ok {
					next.Automatic = v
				}
				if v, ok := in["decisions"]; ok {
					next.Decisions = v
				}
				return next, nil
			})
			if err != nil {
				return err
			}
			p.repositoryChanged()
			return nil
		},
	}
}

// inboxTarget is a new saved inbox, or a saved one by id, on the Inbox. Its
// revision is the inbox list's own (PUT /api/inboxes compares it).
func (p *Proposers) inboxTarget(_ agentcontext.PageContext, name string) (*target, error) {
	api := p.Inboxes
	if api == nil {
		return nil, errors.New("saved inboxes are not available in this daemon")
	}
	id, ok := strings.CutPrefix(name, agentcontext.TargetInbox)
	if !ok || id == "" {
		return nil, fmt.Errorf("the Inbox has no %q that takes proposals", name)
	}
	create := name == agentcontext.TargetNewInbox
	find := func(state inboxes.State) (int, error) {
		i := slices.IndexFunc(state.Inboxes, func(inbox inboxes.Inbox) bool { return inbox.ID == id })
		if i < 0 {
			return -1, fmt.Errorf("there is no saved inbox %s", id)
		}
		return i, nil
	}
	// next is the list the save makes of the stored one: the new inbox
	// added at the end, as the app adds one, or the inbox changed.
	next := func(state inboxes.State, after json.RawMessage) ([]inboxes.Inbox, error) {
		var in agentcontext.InboxChange
		if err := decodeAfter(after, &in); err != nil {
			return nil, err
		}
		if in == (agentcontext.InboxChange{}) {
			return nil, errors.New("propose a name, color, query or badge")
		}
		if in.Query != nil {
			// The app refuses to save a query it cannot read.
			if problem := prfilter.Problem(strings.TrimSpace(*in.Query)); problem != "" {
				return nil, errors.New(problem)
			}
		}
		list := slices.Clone(state.Inboxes)
		var inbox *inboxes.Inbox
		if create {
			if in.Name == nil {
				return nil, errors.New("a new inbox needs a name")
			}
			list = append(list, inboxes.Inbox{Color: "blue", What: "any", Owners: []string{}, Risk: "any", Repositories: []string{}, Badge: true})
			inbox = &list[len(list)-1]
		} else {
			i, err := find(state)
			if err != nil {
				return nil, err
			}
			inbox = &list[i]
		}
		if in.Name != nil {
			inbox.Name = *in.Name
		}
		if in.Color != nil {
			inbox.Color = *in.Color
		}
		if in.Query != nil {
			inbox.Query = *in.Query
		}
		if in.Badge != nil {
			inbox.Badge = *in.Badge
		}
		return list, state.Check(list)
	}
	read := func() (inboxes.State, error) {
		state, err := api.Store.Read()
		if err != nil {
			return state, errors.New("the saved inboxes could not be read")
		}
		return state, nil
	}
	return &target{
		read: func(context.Context) (any, any, error) {
			state, err := read()
			if err != nil {
				return nil, nil, err
			}
			if create {
				return nil, state.Revision, nil
			}
			i, err := find(state)
			if err != nil {
				return nil, nil, err
			}
			return state.Inboxes[i], state.Revision, nil
		},
		validate: func(_ context.Context, after json.RawMessage) error {
			state, err := read()
			if err != nil {
				return err
			}
			_, err = next(state, after)
			return err
		},
		apply: func(_ context.Context, after json.RawMessage, unchanged func(any) error, _ agentcontext.SaveInput) error {
			state, err := read()
			if err != nil {
				return err
			}
			if err := unchanged(state.Revision); err != nil {
				return err
			}
			list, err := next(state, after)
			if err != nil {
				return err
			}
			if p.beforeWrite != nil {
				p.beforeWrite()
			}
			// Replace compares the revision read with the stored one in the
			// transaction that writes.
			_, err = api.Save(state.Revision, list)
			var stale *inboxes.StaleError
			if errors.As(err, &stale) {
				return agentcontext.ErrStale
			}
			return err
		},
	}, nil
}

// groupState is what a group proposal's revision covers.
type groupState struct {
	Name    string
	Members []int
	Stack   bool
	Landed  []reviewgroups.LandedMember
}

// groupTarget is the review group the page shows, saved as
// PATCH /api/groups/{id} saves it.
func (p *Proposers) groupTarget(page agentcontext.PageContext, name string) (*target, error) {
	api := p.Groups
	if api == nil {
		return nil, errors.New("review groups are not available in this daemon")
	}
	id, ok := strings.CutPrefix(name, agentcontext.TargetGroup)
	if !ok || id == "" {
		return nil, fmt.Errorf("a group's page has no %q that takes proposals", name)
	}
	if id != page.Group {
		return nil, errors.New("the person has another group's page open: a proposal is for the group they see")
	}
	stateOf := func(g reviewgroups.Group) groupState { return groupState{g.Name, g.Members, g.Stack, g.Landed} }
	// change is the name and members the save gives the group: nil members
	// when none are added or removed.
	change := func(g reviewgroups.Group, after json.RawMessage) (*string, []int, error) {
		var in agentcontext.GroupChange
		if err := decodeAfter(after, &in); err != nil {
			return nil, nil, err
		}
		if in.Name == nil && len(in.Add) == 0 && len(in.Remove) == 0 {
			return nil, nil, errors.New("propose a name, pull requests to add or pull requests to remove")
		}
		var members []int
		if len(in.Add) > 0 || len(in.Remove) > 0 {
			members = slices.Clone(g.Members)
			for _, n := range in.Add {
				if slices.Contains(members, n) {
					return nil, nil, fmt.Errorf("#%d is in the group already", n)
				}
				members = append(members, n)
			}
			for _, n := range in.Remove {
				i := slices.Index(members, n)
				if i < 0 {
					return nil, nil, fmt.Errorf("#%d is not in the group", n)
				}
				members = slices.Delete(members, i, i+1)
			}
		}
		return in.Name, members, reviewgroups.CheckUpdate(in.Name, members)
	}
	get := func(ctx context.Context) (reviewgroups.Group, error) {
		g, err := api.Store.Get(ctx, id)
		if errors.Is(err, reviewgroups.ErrNotFound) {
			return g, errors.New("the group was not found")
		}
		if err != nil {
			return g, errors.New("the group could not be read")
		}
		return g, nil
	}
	return &target{
		read: func(ctx context.Context) (any, any, error) {
			g, err := get(ctx)
			if err != nil {
				return nil, nil, err
			}
			return struct {
				Name    string `json:"name"`
				Members []int  `json:"members"`
			}{g.Name, g.Members}, stateOf(g), nil
		},
		validate: func(ctx context.Context, after json.RawMessage) error {
			g, err := get(ctx)
			if err != nil {
				return err
			}
			_, _, err = change(g, after)
			return err
		},
		apply: func(ctx context.Context, after json.RawMessage, unchanged func(any) error, _ agentcontext.SaveInput) error {
			_, err := api.Update(ctx, id, func(g reviewgroups.Group) (*string, []int, error) {
				if err := unchanged(stateOf(g)); err != nil {
					return nil, nil, err
				}
				return change(g, after)
			})
			return err
		},
	}, nil
}

// reviewCommentTarget is a comment added to the person's pending review of
// the pull request the page shows: the ui-state "review" record the app
// keeps it in. Its revision is the record's own; the save appends the
// comment and keeps the record's other comments, note and sent reviews.
func (p *Proposers) reviewCommentTarget(page agentcontext.PageContext, name string) (*target, error) {
	store := p.UIState
	if store == nil {
		return nil, errors.New("pending reviews are not available in this daemon")
	}
	repository, number, ok := agentcontext.ParsePullRequestTarget(name)
	if !ok {
		return nil, fmt.Errorf("a pull request's page has no %q that takes proposals", name)
	}
	if !strings.EqualFold(repository, page.Repository) || number != page.PullRequest {
		return nil, errors.New("the person has another pull request's page open: a proposal is for the pull request they see")
	}
	key := strings.ToLower(page.Repository) + "#" + strconv.Itoa(number)
	read := func() (uistate.Record, error) {
		record, err := store.Read("review", key)
		if errors.Is(err, uistate.ErrInvalid) {
			return record, errors.New("this pull request has no pending review")
		}
		if err != nil {
			return record, errors.New("the pending review could not be read")
		}
		return record, nil
	}
	// comment is the comment the save adds, as the app adds one
	// (pending-review.tsx): the pull request in general unless the After
	// names its target.
	comment := func(after json.RawMessage, id string, created int64) (json.RawMessage, error) {
		var in agentcontext.ReviewCommentChange
		if err := decodeAfter(after, &in); err != nil {
			return nil, err
		}
		type targetOf struct {
			Kind  string `json:"kind"`
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		out := struct {
			ID      string   `json:"id"`
			Target  targetOf `json:"target"`
			Body    string   `json:"body"`
			Place   any      `json:"place"`
			Head    string   `json:"head"`
			Created int64    `json:"created"`
		}{ID: id, Target: targetOf{"general", "", "The pull request"}, Body: in.Body, Head: in.Head, Created: created}
		if in.Target != nil {
			out.Target = targetOf{in.Target.Kind, in.Target.ID, in.Target.Title}
		}
		if in.Place != nil {
			out.Place = in.Place
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, err
		}
		if err := uistate.CheckReviewComment(raw); err != nil {
			return nil, errors.New(strings.TrimPrefix(err.Error(), uistate.ErrInvalid.Error()+": "))
		}
		return raw, nil
	}
	// review is the record's value as fields, its comments apart, so the
	// save keeps every other field as stored.
	review := func(record uistate.Record) (map[string]json.RawMessage, []json.RawMessage, error) {
		fields := map[string]json.RawMessage{}
		if len(record.Value) > 0 && string(record.Value) != "null" {
			if err := json.Unmarshal(record.Value, &fields); err != nil {
				return nil, nil, errors.New("the pending review could not be read")
			}
		}
		var comments []json.RawMessage
		if raw, ok := fields["comments"]; ok {
			if err := json.Unmarshal(raw, &comments); err != nil {
				return nil, nil, errors.New("the pending review could not be read")
			}
		}
		if _, ok := fields["note"]; !ok {
			fields["note"] = json.RawMessage(`""`)
		}
		if len(comments) >= uistate.MaxReviewComments {
			return nil, nil, fmt.Errorf("a pending review holds at most %d comments", uistate.MaxReviewComments)
		}
		return fields, comments, nil
	}
	return &target{
		read: func(context.Context) (any, any, error) {
			record, err := read()
			if err != nil {
				return nil, nil, err
			}
			// The comment is new: there is no current value of it.
			return nil, record.Revision, nil
		},
		validate: func(_ context.Context, after json.RawMessage) error {
			record, err := read()
			if err != nil {
				return err
			}
			if _, _, err := review(record); err != nil {
				return err
			}
			_, err = comment(after, "proposal", 0)
			return err
		},
		apply: func(_ context.Context, after json.RawMessage, unchanged func(any) error, _ agentcontext.SaveInput) error {
			record, err := read()
			if err != nil {
				return err
			}
			if err := unchanged(record.Revision); err != nil {
				return err
			}
			fields, comments, err := review(record)
			if err != nil {
				return err
			}
			id := make([]byte, 16)
			if _, err := rand.Read(id); err != nil {
				return err
			}
			added, err := comment(after, hex.EncodeToString(id), time.Now().UnixMilli())
			if err != nil {
				return err
			}
			if fields["comments"], err = json.Marshal(append(comments, added)); err != nil {
				return err
			}
			value, err := json.Marshal(fields)
			if err != nil {
				return err
			}
			if p.beforeWrite != nil {
				p.beforeWrite()
			}
			// Replace compares the record's revision with the one read in the
			// transaction that writes.
			_, err = store.Replace("review", key, record.Revision, p.mutation(), value)
			var stale *uistate.StaleError
			if errors.As(err, &stale) {
				return agentcontext.ErrStale
			}
			if errors.Is(err, uistate.ErrInvalid) {
				return errors.New("the pending review would not be valid with the comment")
			}
			return err
		},
	}, nil
}
