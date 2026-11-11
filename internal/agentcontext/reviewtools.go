package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/baum-ai/baum/review"
)

// The review tools read a review in parts, one per call, so the document is
// never sent whole. No tool here marshals the document it read: each builds
// its result from the fields of its part.

// groupProperty is the `group` argument of the tools that read a group's
// review.
const groupProperty = `"group":{"type":"string","description":"Read a review group's combined review instead of the pull request's: a group id, or \"this\" for the open group page's, or for an action the one group its pull request is in."}`

// reviewSchema is the schema of a review tool: the pull request, a group and
// a version, and the properties extra adds.
func reviewSchema(extra string, required ...string) json.RawMessage {
	properties := groupProperty + `,` + `"version":{"type":"integer","minimum":1,"description":"A saved version. Defaults to the latest."}`
	if extra != "" {
		properties += "," + extra
	}
	return targetSchema(properties, required...)
}

// reviewArgs are the arguments of the review tools.
type reviewArgs struct {
	pullArgs
	Group   string `json:"group"`
	Version int    `json:"version"`
	ID      string `json:"id"`
	Part    string `json:"part"`
}

// reviewTools are the tools that read a pull request's review, or a group's.
func (s *Server) reviewTools() []Tool {
	index := "/api/index"
	return []Tool{{
		Name:        "facts",
		Description: "The pull request's facts, which the app shows before any review exists: change size, checks, sessions, conversation, threads, hunks and the agent reviews posted on it. Nothing a model wrote.",
		InputSchema: targetSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/facts"},
		Call:        callFacts,
	}, {
		Name:        "review_outline",
		Description: "The overview of a saved review and an outline of each section: id, title, kind, the hunks and files it covers, assessment, and whether its details are pending or failed. Never a section's details: read those with review_section.",
		InputSchema: reviewSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/document", "/api/groups"},
		Call:        callReviewOutline,
	}, {
		Name:        "review_section",
		Description: "One section of a saved review by its id: its summary, risks, behaviours, evidence, citations and whatever else it holds. Says when its details failed or are still being written.",
		InputSchema: reviewSchema(`"id":{"type":"string","description":"The section's id, from review_outline."}`, "id"),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/document", "/api/groups"},
		Call:        callReviewSection,
	}, {
		Name:        "review_context",
		Description: "The codebase context of a saved review, or that it has none or could not write it.",
		InputSchema: reviewSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/document", "/api/groups", "/api/jobs"},
		Call:        callReviewContext,
	}, {
		Name:        "review_draft",
		Description: "A part of the review of this pull request that is being written now (drafts are never stored): the outline, one section by id, or the context. Says so when no review is running.",
		InputSchema: targetSchema(`"part":{"type":"string","enum":["outline","section","context"],"description":"Which part. Defaults to the outline."},"id":{"type":"string","description":"The section's id, for part section."}`),
		Scope:       ScopeRepository,
		Reads:       []string{"/api/jobs"},
		Call:        callReviewDraft,
	}, {
		Name:        "review_versions",
		Description: "The saved versions of the pull request's review, or of a group's: version, heads, when and by what model it was generated, and its recorded cost.",
		InputSchema: targetSchema(groupProperty),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/versions", "/api/groups"},
		Call:        callReviewVersions,
	}, {
		Name:        "review_conversations",
		Description: "The user's conversations with the agent that wrote the pull request's review. Without an id, a list: id, version, times, number of turns and the first question. With an id, that conversation's turns: question, focus, answer, citations, state and note.",
		InputSchema: targetSchema(`"id":{"type":"string","description":"A conversation's id, from the list."}`),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/prs/chat"},
		Call:        callReviewConversations,
	}}
}

func callFacts(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return "", err
	}
	query, err := e.selector()
	if err != nil {
		return "", err
	}
	if e.HeadSHA != "" {
		query.Set("head", e.HeadSHA)
	}
	body, missing, err := c.get(ctx, "/api/facts", query)
	if err != nil {
		return "", err
	}
	if missing {
		return "", errors.New("the pull request's facts are not available")
	}
	return string(body), nil
}

// reviewSource is a saved review document and where it is from.
type reviewSource struct {
	doc json.RawMessage
	// from names it in a result.
	from map[string]any
	// repository, number and group say what was read; number is 0 for a group.
	repository string
	number     int
	group      string
}

// reviewDocument reads the review a call names: the group's combined review
// when it names a group (or a chat has a group page open), else the pull
// request's own, the latest version or the one asked for.
func (c CallContext) reviewDocument(ctx context.Context, a reviewArgs) (reviewSource, error) {
	group := a.Group
	if group == "" && c.Consumer.Kind != Action && c.HasPage && c.Page.Page == PageGroup && a.PR == 0 {
		group = "this"
	}
	query := url.Values{}
	if a.Version > 0 {
		query.Set("version", strconv.Itoa(a.Version))
	}
	if group != "" {
		if group == "this" {
			group = ""
		}
		id, _, err := groupFor(ctx, c, a.Repository, group)
		if err != nil {
			return reviewSource{}, err
		}
		doc, missing, err := c.get(ctx, "/api/groups/"+url.PathEscape(id)+"/document", query)
		if err != nil {
			return reviewSource{}, err
		}
		if missing {
			return reviewSource{}, fmt.Errorf("group %s has no saved review (of that version)", id)
		}
		return reviewSource{doc: doc, from: map[string]any{"group": id}, group: id}, nil
	}
	repository, number, err := c.pullTarget(mustJSON(a.pullArgs))
	if err != nil {
		return reviewSource{}, err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return reviewSource{}, err
	}
	if e.VersionCount == 0 {
		return reviewSource{}, errors.New("the pull request has no saved review yet: read its facts")
	}
	selector, err := e.selector()
	if err != nil {
		return reviewSource{}, err
	}
	for k, v := range query {
		selector[k] = v
	}
	doc, missing, err := c.get(ctx, "/api/document", selector)
	if err != nil {
		return reviewSource{}, err
	}
	if missing {
		return reviewSource{}, errors.New("the pull request has no such saved review")
	}
	return reviewSource{doc: doc, from: map[string]any{"repository": repository, "pull_request": number}, repository: repository, number: number}, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// reviewHeader is what every part says of the review it is from.
type reviewHeader struct {
	Version     int    `json:"version"`
	Head        string `json:"head"`
	Base        string `json:"base"`
	GeneratedAt string `json:"generated_at,omitempty"`
}

// parsed is the few fields of a review document the projections read.
type parsed struct {
	Review  reviewHeader `json:"review"`
	Summary struct {
		Context json.RawMessage `json:"context"`
	} `json:"summary"`
	Sections []json.RawMessage `json:"sections"`
	Index    struct {
		Hunks []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"hunks"`
	} `json:"index"`
}

type sectionHead struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Details string `json:"details"`
}

// outlineView is the outline of a review document: the outline stage's own
// projection (review.ReadOutline), each section with its details state and
// the files its hunks are in.
func outlineView(doc []byte) (map[string]any, error) {
	outline, err := review.ReadOutline(doc)
	if err != nil {
		return nil, errors.New("the review is not readable")
	}
	var p parsed
	if json.Unmarshal(doc, &p) != nil {
		return nil, errors.New("the review is not readable")
	}
	var o struct {
		Summary   json.RawMessage              `json:"summary"`
		Sections  []map[string]json.RawMessage `json:"sections"`
		Relations json.RawMessage              `json:"relations"`
	}
	if err := json.Unmarshal(outline, &o); err != nil {
		return nil, errors.New("the review is not readable")
	}
	state := map[string]string{}
	for _, raw := range p.Sections {
		var h sectionHead
		if json.Unmarshal(raw, &h) == nil {
			state[h.ID] = h.Details
		}
	}
	paths := map[string]string{}
	for _, h := range p.Index.Hunks {
		paths[h.ID] = h.Path
	}
	sections := make([]map[string]any, 0, len(o.Sections))
	for _, raw := range o.Sections {
		section := map[string]any{}
		for k, v := range raw {
			section[k] = v
		}
		var id string
		_ = json.Unmarshal(raw["id"], &id)
		if d := state[id]; d != "" {
			section["details"] = d
		}
		var hunks []string
		_ = json.Unmarshal(raw["hunks"], &hunks)
		files := []string{}
		for _, h := range hunks {
			if path := paths[h]; path != "" && !slices.Contains(files, path) {
				files = append(files, path)
			}
		}
		section["files"] = files
		sections = append(sections, section)
	}
	out := map[string]any{"review": p.Review, "overview": o.Summary, "sections": sections}
	if len(o.Relations) > 0 && string(o.Relations) != "null" {
		out["relations"] = o.Relations
	}
	return out, nil
}

// sectionView is one section of a review document by id: all it holds, or,
// for a section whose details are failed or pending, only its outline and
// that state, said in words.
func sectionView(doc []byte, id string) (map[string]any, error) {
	var p parsed
	if json.Unmarshal(doc, &p) != nil {
		return nil, errors.New("the review is not readable")
	}
	var ids []string
	for _, raw := range p.Sections {
		var h sectionHead
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		ids = append(ids, h.ID)
		if h.ID != id {
			continue
		}
		var all map[string]json.RawMessage
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, errors.New("the section is not readable")
		}
		out := map[string]any{"review": p.Review}
		switch h.Details {
		case "failed", "pending":
			// Only the outline fields are meaningful; the rest is empty.
			for _, key := range review.OutlineSectionFields() {
				if v, ok := all[key]; ok {
					out[key] = v
				}
			}
			out["details"] = h.Details
			if h.Details == "failed" {
				out["note"] = "The details of this section could not be written, so it has no summary, risks or evidence. Read the diff of its hunks instead."
			} else {
				out["note"] = "The details of this section are still being written."
			}
		default:
			section := map[string]any{}
			for k, v := range all {
				section[k] = v
			}
			out["section"] = section
		}
		return out, nil
	}
	return nil, fmt.Errorf("no section %q in the review; its sections are %s", id, strings.Join(ids, ", "))
}

// contextView is the summary's codebase context of a review document, or
// nil when it has none.
func contextView(doc []byte) (json.RawMessage, reviewHeader, error) {
	var p parsed
	if json.Unmarshal(doc, &p) != nil {
		return nil, reviewHeader{}, errors.New("the review is not readable")
	}
	if len(p.Summary.Context) == 0 || string(p.Summary.Context) == "null" {
		return nil, p.Review, nil
	}
	return p.Summary.Context, p.Review, nil
}

func callReviewOutline(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	_, src, err := c.reviewRead(ctx, args)
	if err != nil {
		return "", err
	}
	out, err := outlineView(src.doc)
	if err != nil {
		return "", err
	}
	out["from"] = src.from
	return compactJSON(out)
}

func callReviewSection(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	a, src, err := c.reviewRead(ctx, args)
	if err != nil {
		return "", err
	}
	out, err := sectionView(src.doc, a.ID)
	if err != nil {
		return "", err
	}
	out["from"] = src.from
	return compactJSON(out)
}

func callReviewContext(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	_, src, err := c.reviewRead(ctx, args)
	if err != nil {
		return "", err
	}
	context, header, err := contextView(src.doc)
	if err != nil {
		return "", err
	}
	out := map[string]any{"review": header, "from": src.from}
	if context != nil {
		out["context"] = context
		return compactJSON(out)
	}
	out["state"] = "absent"
	out["note"] = "The review has no codebase context: it was generated before context existed, or without it."
	if src.number > 0 && c.contextFailed(ctx, src.repository, src.number, header.Head) {
		out["state"] = "failed"
		out["note"] = "The review's codebase context could not be written, so it was published without it."
	}
	return compactJSON(out)
}

// contextFailed reports whether the pull request's review job of this head
// published without its context.
func (c CallContext) contextFailed(ctx context.Context, repository string, number int, head string) bool {
	body, _, err := c.get(ctx, "/api/jobs", pullQuery(repository, number))
	if err != nil {
		return false
	}
	var list struct {
		Jobs []struct {
			State         string `json:"state"`
			HeadSHA       string `json:"head_sha"`
			ContextFailed bool   `json:"context_failed"`
		} `json:"jobs"`
	}
	if json.Unmarshal(body, &list) != nil {
		return false
	}
	for _, j := range list.Jobs {
		// As the page does: only a completed job says its context failed.
		if j.State == "completed" && j.ContextFailed && j.HeadSHA == head {
			return true
		}
	}
	return false
}

// reviewRead decodes a review tool's arguments and reads its document.
func (c CallContext) reviewRead(ctx context.Context, args json.RawMessage) (reviewArgs, reviewSource, error) {
	var a reviewArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, reviewSource{}, err
	}
	src, err := c.reviewDocument(ctx, a)
	return a, src, err
}

const noRunningReview = "No running review of this pull request."

func callReviewDraft(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a reviewArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	part := a.Part
	if part == "" {
		part = "outline"
	}
	if part != "outline" && part != "section" && part != "context" {
		return "", fmt.Errorf("part %q is not outline, section or context", part)
	}
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	body, _, err := c.get(ctx, "/api/jobs", pullQuery(repository, number))
	if err != nil {
		return "", err
	}
	var list struct {
		Available *bool `json:"available"`
		Jobs      []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			State string `json:"state"`
			Group string `json:"group"`
			Draft int    `json:"draft"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", errors.New("the jobs are not readable, so whether a review is running is unknown")
	}
	if list.Available != nil && !*list.Available {
		return "", errors.New("the jobs are unavailable, so whether a review is running is unknown")
	}
	job := ""
	drafts := 0
	for _, j := range list.Jobs {
		// A review of this pull request alone, being written now.
		if j.State == "running" && j.Kind == "" && j.Group == "" {
			job, drafts = j.ID, j.Draft
		}
	}
	if job == "" {
		return noRunningReview, nil
	}
	if drafts == 0 {
		return "A review of this pull request is running but has not written a draft yet.", nil
	}
	body, missing, err := c.get(ctx, "/api/jobs/"+url.PathEscape(job)+"/draft", nil)
	if err != nil {
		return "", err
	}
	if missing {
		// It ended, or has no draft now: drafts are never kept.
		return noRunningReview, nil
	}
	var draft struct {
		Draft    json.RawMessage `json:"draft"`
		Document json.RawMessage `json:"document"`
	}
	if json.Unmarshal(body, &draft) != nil || len(draft.Document) == 0 {
		return "", errors.New("the draft is not readable")
	}
	out := map[string]any{"draft": draft.Draft, "part": part, "note": "A review being written: parts not written yet are pending, and the draft changes until the review is saved."}
	switch part {
	case "outline":
		view, err := outlineView(draft.Document)
		if err != nil {
			return "", err
		}
		for k, v := range view {
			out[k] = v
		}
	case "section":
		if a.ID == "" {
			return "", errors.New("name a section: part section needs its id")
		}
		view, err := sectionView(draft.Document, a.ID)
		if err != nil {
			return "", err
		}
		for k, v := range view {
			out[k] = v
		}
	case "context":
		context, _, err := contextView(draft.Document)
		if err != nil {
			return "", err
		}
		if context == nil {
			out["state"] = "pending"
			out["note"] = "The codebase context is not written yet, or could not be."
		} else {
			out["context"] = context
		}
	}
	return compactJSON(out)
}

// versionFields are what the app shows of a saved version: never the session
// that wrote it, the accounting, digests or policy.
var versionFields = []string{"version", "head", "base", "merge_base", "generated_at", "backend", "model", "recorded_cost_usd", "cost_reason"}

func callReviewVersions(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a reviewArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	group := a.Group
	if group == "" && c.Consumer.Kind != Action && c.HasPage && c.Page.Page == PageGroup && a.PR == 0 {
		group = "this"
	}
	var body json.RawMessage
	out := map[string]any{}
	if group != "" {
		if group == "this" {
			group = ""
		}
		id, _, err := groupFor(ctx, c, a.Repository, group)
		if err != nil {
			return "", err
		}
		b, missing, err := c.get(ctx, "/api/groups/"+url.PathEscape(id)+"/versions", nil)
		if err != nil {
			return "", err
		}
		if missing {
			return "", fmt.Errorf("group %s has no saved versions", id)
		}
		body, out["group"] = b, id
	} else {
		repository, number, err := c.pullTarget(args)
		if err != nil {
			return "", err
		}
		e, err := c.entry(ctx, repository, number)
		if err != nil {
			return "", err
		}
		query, err := e.selector()
		if err != nil {
			return "", err
		}
		b, missing, err := c.get(ctx, "/api/versions", query)
		if err != nil {
			return "", err
		}
		if missing {
			return "", errors.New("the pull request has no saved versions")
		}
		body, out["repository"], out["pull_request"] = b, repository, number
	}
	var all []map[string]json.RawMessage
	if err := json.Unmarshal(body, &all); err != nil {
		return "", errors.New("the versions are not readable")
	}
	versions := make([]map[string]json.RawMessage, 0, len(all))
	for _, v := range all {
		kept := map[string]json.RawMessage{}
		for _, field := range versionFields {
			if value, ok := v[field]; ok {
				kept[field] = value
			}
		}
		versions = append(versions, kept)
	}
	out["versions"] = versions
	return compactJSON(out)
}

const maxQuestionPreview = 200

func callReviewConversations(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a reviewArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	repository, number, err := c.pullTarget(args)
	if err != nil {
		return "", err
	}
	e, err := c.entry(ctx, repository, number)
	if err != nil {
		return "", err
	}
	query, err := e.selector()
	if err != nil {
		return "", err
	}
	body, missing, err := c.get(ctx, "/api/prs/chat", query)
	if err != nil {
		return "", err
	}
	if missing {
		return "", errors.New("the pull request has no conversations route")
	}
	var list struct {
		Available     *bool `json:"available"`
		Conversations []struct {
			ID      string          `json:"id"`
			Version int             `json:"version"`
			Created json.RawMessage `json:"created"`
			Updated json.RawMessage `json:"updated"`
			Turns   []struct {
				Seq      int             `json:"seq"`
				Question string          `json:"question"`
				Focus    json.RawMessage `json:"focus"`
				Answer   json.RawMessage `json:"answer"`
				State    string          `json:"state"`
				Reason   string          `json:"reason"`
				Note     string          `json:"note"`
				Created  json.RawMessage `json:"created"`
			} `json:"turns"`
		} `json:"conversations"`
	}
	if json.Unmarshal(body, &list) != nil || (list.Available != nil && !*list.Available) {
		return "", errors.New("conversations are unavailable")
	}
	if a.ID == "" {
		rows := make([]map[string]any, 0, len(list.Conversations))
		for _, conv := range list.Conversations {
			row := map[string]any{"id": conv.ID, "version": conv.Version, "created": conv.Created, "updated": conv.Updated, "turns": len(conv.Turns)}
			if len(conv.Turns) > 0 {
				row["first_question"] = truncateRunes(conv.Turns[0].Question, maxQuestionPreview)
			}
			rows = append(rows, row)
		}
		return compactJSON(map[string]any{"repository": repository, "pull_request": number, "conversations": rows})
	}
	for _, conv := range list.Conversations {
		if conv.ID != a.ID {
			continue
		}
		turns := make([]map[string]any, 0, len(conv.Turns))
		for _, t := range conv.Turns {
			turn := map[string]any{"seq": t.Seq, "question": t.Question, "state": t.State, "created": t.Created}
			if len(t.Focus) > 0 && string(t.Focus) != "null" {
				turn["focus"] = t.Focus
			}
			if t.Reason != "" {
				turn["reason"] = t.Reason
			}
			if t.Note != "" {
				turn["note"] = t.Note
			}
			if len(t.Answer) > 0 {
				var answer struct {
					Markdown    string          `json:"answer_markdown"`
					Citations   json.RawMessage `json:"citations"`
					SuggestsFix json.RawMessage `json:"suggests_fix"`
				}
				if json.Unmarshal(t.Answer, &answer) == nil {
					turn["answer_markdown"] = answer.Markdown
					if len(answer.Citations) > 0 {
						turn["citations"] = answer.Citations
					}
					if len(answer.SuggestsFix) > 0 && string(answer.SuggestsFix) != "null" {
						turn["suggests_fix"] = answer.SuggestsFix
					}
				}
			}
			turns = append(turns, turn)
		}
		return compactJSON(map[string]any{"repository": repository, "pull_request": number, "id": conv.ID, "version": conv.Version, "created": conv.Created, "updated": conv.Updated, "turns": turns})
	}
	return "", fmt.Errorf("no conversation %q of the pull request", a.ID)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}
