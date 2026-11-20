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

	"github.com/baum-ai/baum/reviewv2"
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
		Description: "The overview of a saved review (summary, verdict, why the risk is what it is, risk score, reading time, screenshots, main diagram, intent) and an outline of each section: id, title, kind, gist, level, the hunks and files it covers, the ids of the tiles on it, and whether its details are pending or failed. Never a section's body or risks: read those with review_section.",
		InputSchema: reviewSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/tiles", "/api/groups"},
		Call:        callReviewOutline,
	}, {
		Name:        "review_section",
		Description: "One section of a saved review by its id: its outline, the reasons for its level, its body (blocks of prose, points with their detail and citations, notes, and the tiles it places by id) and risks with their citations, and the tiles on it (id, type, title, caption, state; never their data). Says when its details failed or are still being written.",
		InputSchema: reviewSchema(`"id":{"type":"string","description":"The section's id, from review_outline."}`, "id"),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/tiles", "/api/groups"},
		Call:        callReviewSection,
	}, {
		Name:        "review_context",
		Description: "Where the change of a saved review fits in the codebase (area, how it is reached, contracts it relies on, related code, each with its files), or that it has none or could not write it.",
		InputSchema: reviewSchema(""),
		Scope:       ScopeRepository,
		Reads:       []string{index, "/api/tiles", "/api/groups", "/api/jobs"},
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
// request's own, the latest version or the one asked for, as the tile route
// serves it (a baum.review/2 document, whatever schema it was stored in).
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
	body, missing, err := c.get(ctx, "/api/tiles", selector)
	if err != nil {
		return reviewSource{}, err
	}
	if missing {
		return reviewSource{}, errors.New("the pull request has no such saved review")
	}
	// The tile route serves every version as a baum.review/2 document.
	var served struct {
		Document json.RawMessage `json:"document"`
	}
	if json.Unmarshal(body, &served) != nil || len(served.Document) == 0 {
		return reviewSource{}, errors.New("the review is not readable")
	}
	doc := served.Document
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

// readReview reads a review document as baum.review/2: the tile route's
// document as it is, a group's baum.review/1 document converted
// (reviewv2.Read). An unreadable document is an error that quotes nothing of
// it.
func readReview(doc []byte) (reviewv2.Document, reviewHeader, error) {
	d, err := reviewv2.Read(doc)
	if err != nil {
		return reviewv2.Document{}, reviewHeader{}, errors.New("the review is not readable")
	}
	var h reviewHeader
	_ = json.Unmarshal(d.Review, &h)
	return d, h, nil
}

// sectionFiles are the files a section's hunks are in, once each.
func sectionFiles(d reviewv2.Document, hunks []string) []string {
	var index struct {
		Hunks []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"hunks"`
	}
	_ = json.Unmarshal(d.Index, &index)
	paths := map[string]string{}
	for _, h := range index.Hunks {
		paths[h.ID] = h.Path
	}
	files := []string{}
	for _, h := range hunks {
		if path := paths[h]; path != "" && !slices.Contains(files, path) {
			files = append(files, path)
		}
	}
	return files
}

// ownedTiles are the tiles a section, "context" or "overview" owns, by ID.
func ownedTiles(d reviewv2.Document, owner string) []string {
	ids := []string{}
	for _, t := range d.Tiles {
		if t.Owner == owner {
			ids = append(ids, t.ID)
		}
	}
	return ids
}

// sectionOutline is a section's outline: what the map wrote of it, the
// files its hunks are in, the tiles on it and whether its details are
// pending or failed.
func sectionOutline(d reviewv2.Document, s reviewv2.Section) map[string]any {
	out := map[string]any{"id": s.ID, "title": s.Title, "kind": s.Kind, "gist": s.Gist, "level": s.Level, "hunks": s.Hunks, "files": sectionFiles(d, s.Hunks), "tiles": ownedTiles(d, s.ID)}
	if s.Scope != "" {
		out["scope"] = s.Scope
	}
	if s.Details != "" {
		out["details"] = s.Details
	}
	return out
}

// outlineView is the outline of a review: its overview (the summary, the
// verdict, why the risk is what it is, the risk score, the reading time,
// the screenshots, main diagram and intent) and each section's outline,
// never a section's body or risks.
func outlineView(doc []byte) (map[string]any, error) {
	d, header, err := readReview(doc)
	if err != nil {
		return nil, err
	}
	sections := make([]map[string]any, 0, len(d.Sections))
	for _, s := range d.Sections {
		sections = append(sections, sectionOutline(d, s))
	}
	overview := map[string]any{"title": d.Summary.Title, "text": d.Summary.Text, "change_kind": d.Summary.ChangeKind, "verdict": d.Summary.Verdict, "risk_why": d.Summary.RiskWhy,
		"risk_score": d.Summary.RiskScore, "review_minutes": d.Summary.ReviewMinutes, "screenshots": d.Overview.Screenshots, "diagram": d.Overview.Diagram, "intent": d.Overview.Intent}
	out := map[string]any{"review": header, "overview": overview, "sections": sections}
	var relations struct {
		Relations json.RawMessage `json:"relations"`
	}
	if json.Unmarshal(d.Review, &relations) == nil && len(relations.Relations) > 0 && string(relations.Relations) != "null" && string(relations.Relations) != "[]" {
		out["relations"] = relations.Relations
	}
	return out, nil
}

// tileSummary is what a section part says of a tile on it: never its data.
func tileSummary(t reviewv2.Tile) map[string]any {
	out := map[string]any{"id": t.ID, "type": t.Type, "title": t.Title, "state": t.State}
	if t.Caption != "" {
		out["caption"] = t.Caption
	}
	if t.Shows != "" {
		out["shows"] = t.Shows
	}
	if t.Added {
		out["added"] = true
	}
	return out
}

// sectionView is one section of a review by id: its outline, body,
// risks and the tiles on it, or, for a section whose details are
// failed or pending, only its outline and that state, said in words.
func sectionView(doc []byte, id string) (map[string]any, error) {
	d, header, err := readReview(doc)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range d.Sections {
		ids = append(ids, s.ID)
		if s.ID != id {
			continue
		}
		out := map[string]any{"review": header}
		switch s.Details {
		case reviewv2.DetailsFailed, reviewv2.DetailsPending:
			// Only the outline is meaningful; the rest is empty.
			for k, v := range sectionOutline(d, s) {
				out[k] = v
			}
			out["level_reasons"] = s.LevelReasons
			if s.Details == reviewv2.DetailsFailed {
				out["note"] = "The details of this section could not be written, so it has no body or risks. Read the diff of its hunks instead."
			} else {
				out["note"] = "The details of this section are still being written."
			}
		default:
			section := sectionOutline(d, s)
			section["level_reasons"], section["body"], section["risks"] = s.LevelReasons, s.Body, s.Risks
			var tiles []map[string]any
			for _, t := range d.Tiles {
				if t.Owner == s.ID {
					tiles = append(tiles, tileSummary(t))
				}
			}
			section["tiles"] = tiles
			out["section"] = section
		}
		return out, nil
	}
	return nil, fmt.Errorf("no section %q in the review; its sections are %s", id, strings.Join(ids, ", "))
}

// contextView is where a review's change fits, or nil when it has none.
func contextView(doc []byte) (json.RawMessage, reviewHeader, error) {
	d, header, err := readReview(doc)
	if err != nil {
		return nil, reviewHeader{}, err
	}
	if d.Context == nil {
		return nil, header, nil
	}
	encoded, err := json.Marshal(d.Context)
	if err != nil {
		return nil, header, errors.New("the review is not readable")
	}
	return encoded, header, nil
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
	body, missing, err := c.get(ctx, "/api/jobs/"+url.PathEscape(job)+"/draft", url.Values{"tiles": {"1"}})
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
