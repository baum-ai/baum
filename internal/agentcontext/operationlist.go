package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/reviewindex"
)

// The operations a chat may propose, each on the pages whose buttons do it,
// and each sending the request that button sends. Build reads what the
// button's own code reads (the review index for the head the page shows, the
// merge options for the method, the clone the app would pick) and refuses
// what the app would refuse; Describe checks that the target still belongs
// to the page, and says what will happen. The route's own validation applies
// on Confirm. Not here, and why:
//
//   - Marking a pull request seen in the Inbox: the Inbox's seen state is the
//     app's own UI state (PUT /api/ui-state/inbox, a whole revisioned value
//     the app computes); no route marks one pull request seen.
//     POST /api/jobs/{id}/seen is the tray's acknowledgement of a failed job,
//     which the app offers nowhere.
//   - Rerunning a job, or starting one, from the Jobs pages: the app has no
//     such button there. A review and a fix start from the pages that offer
//     them (review, fix).
//   - Registering a local folder (POST /api/repos): the folder is the
//     person's choice, from their file picker.
//   - Trying a draft prompt: no route runs a review with draft prompt text.
func operations() []Operation {
	return slices.Concat(pullOperations(), sessionOperations(), jobOperations(), groupOperations(), repositoryOperations())
}

// errAlreadyRunning is a job of the kind asked for that is already queued
// or running, which the app's button refuses.
var errAlreadyRunning = errors.New("one is already queued or running for this pull request")

// callContext is the CallContext the tool helpers read with.
func (c OperationContext) callContext() CallContext {
	return CallContext{Consumer: c.Consumer, Page: c.Page, HasPage: true, Reader: c.Reader}
}

// get reads path, with query, through the operation's reader.
func (c OperationContext) get(ctx context.Context, path string, query url.Values) (json.RawMessage, bool, error) {
	return c.callContext().get(ctx, path, query)
}

// A summary is baum's own sentence (Action) and fields. Text that is not
// baum's (what a pull request, session, thread or tool says, and what the
// agent wrote) is never written into the sentence: it is a quoted field of
// its own, shown in full. Text read from the daemon's routes is made plain
// for display (plain); text the agent wrote is refused at Build when it
// holds anything plain would change (checkAgentText), so the field shows
// exactly what will be sent.

// hidden reports whether r is a character a summary must not carry: a
// control character (newline and tab apart, where text is multi-line), a
// format character (bidirectional overrides and isolates, zero-width
// characters), or a line or paragraph separator outside multi-line text.
func hidden(r rune, multiline bool) bool {
	switch {
	case multiline && (r == '\n' || r == '\t'):
		return false
	case unicode.IsControl(r), r == utf8.RuneError, invisible(r):
		return true
	case unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
		// Line and paragraph separators break lines where the app may not:
		// refused in the agent's text, a space in text read from a route.
		return true
	}
	return false
}

// invisible reports whether r is a default-ignorable code point, which
// renders as nothing or changes how the text around it renders: format
// characters (Cf: bidirectional controls, zero-width characters, the soft
// hyphen, tags), variation selectors and the other default-ignorables (the
// combining grapheme joiner U+034F, the Hangul fillers U+115F, U+1160, U+3164
// and U+FFA0, the Khmer inherent vowels U+17B4 and U+17B5). Go's tables have
// no Default_Ignorable_Code_Point property; these three are what it is made
// of, and the named ones are listed again so a table change cannot drop
// them.
func invisible(r rune) bool {
	switch r {
	case 0x034F, 0x115F, 0x1160, 0x17B4, 0x17B5, 0x3164, 0xFFA0:
		return true
	}
	return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r)
}

// plain is untrusted text as a summary shows it: valid UTF-8, with each
// hidden character replaced (a line break by a space in one-line text, the
// rest dropped), and carriage returns of multi-line text turned into line
// breaks.
func plain(text string, multiline bool) string {
	text = strings.ToValidUTF8(text, "�")
	if multiline {
		text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	}
	var b strings.Builder
	for _, r := range text {
		switch {
		case !hidden(r, multiline):
			b.WriteRune(r)
		case r == '\n' || r == '\r' || r == '\t' || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r):
			b.WriteByte(' ')
		}
	}
	return strings.TrimSpace(b.String())
}

// checkAgentText refuses text the agent wrote that a summary could not show
// as it is: invalid UTF-8, control characters (a line break or tab apart in
// multi-line text) or format characters such as bidirectional overrides.
func checkAgentText(what, text string, multiline bool) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("the %s is not valid UTF-8", what)
	}
	if strings.ContainsRune(text, '\r') {
		return fmt.Errorf("the %s holds a carriage return: use line breaks (\\n)", what)
	}
	for _, r := range text {
		if hidden(r, multiline) {
			return fmt.Errorf("the %s holds a control or invisible formatting character (%U), which the person could not see", what, r)
		}
	}
	return nil
}

// fact is a field in baum's own words.
func fact(label, value string) SummaryField { return SummaryField{Label: label, Value: value} }

// quoted is a field of untrusted text read from a route, made plain and
// shown in full; empty text is no field.
func quoted(fields []SummaryField, label, value string, multiline bool) []SummaryField {
	if value = plain(value, multiline); value == "" {
		return fields
	}
	return append(fields, SummaryField{Label: label, Value: value, Quoted: true, Multiline: multiline})
}

// agentWrote is a field of text the agent wrote, already checked at Build
// (checkAgentText): shown in full, exactly as it will be sent.
func agentWrote(label, value string, multiline bool) SummaryField {
	return SummaryField{Label: label, Value: value, Quoted: true, Multiline: multiline, ByAgent: true}
}

// baum's sentence and the fields' labels name a thing only by an identifier
// that passes its own check (a repository spelled as GitHub spells one, an
// id that is one plain path segment); one that does not is named generically
// and quoted in a field of its own.

// repositoryNamed is a repository as baum's sentence names it.
func repositoryNamed(fields []SummaryField, repository string) (string, []SummaryField) {
	if validRepository(repository) {
		return repository, fields
	}
	return "a repository", quoted(fields, "Repository", repository, false)
}

// setting is a value of the person's own settings (a harness, model or
// effort), one plain line.
func setting(s string) string { return plain(s, false) }

// headField is the head a write is pinned to.
func headField(fields []SummaryField, sha string) []SummaryField {
	if hexSHA(sha) {
		return append(fields, fact("At head", shortSHA(sha)))
	}
	return quoted(fields, "At head", sha, false)
}

func hexSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func pullKey(repository string, number int) string {
	return strings.ToLower(repository) + "#" + strconv.Itoa(number)
}

// parsePullKey splits an "owner/name#N" target.
func parsePullKey(target string) (string, int, bool) {
	i := strings.LastIndexByte(target, '#')
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(target[i+1:])
	return target[:i], n, err == nil && n > 0
}

// pageOwnPull is the pull request a pull request's page shows.
func pageOwnPull(c OperationContext) (string, int, error) {
	if c.Page.Page != PagePullRequest || c.Page.Repository == "" || c.Page.PullRequest < 1 {
		return "", 0, ErrNotCurrentPage
	}
	return c.Page.Repository, c.Page.PullRequest, nil
}

// pullOnPage finds a pull request the open page shows: on a pull request's
// page its own (a pull request named must be it), on the Inbox one the
// Inbox lists, on Pull requests one the list shows. It returns its review
// index entry.
func pullOnPage(ctx context.Context, c OperationContext, repository string, number int) (reviewEntry, error) {
	cc := c.callContext()
	repository = strings.TrimSpace(repository)
	switch c.Page.Page {
	case PagePullRequest:
		own, n, err := pageOwnPull(c)
		if err != nil {
			return reviewEntry{}, err
		}
		if (repository != "" && !strings.EqualFold(repository, own)) || (number != 0 && number != n) {
			return reviewEntry{}, fmt.Errorf("the person has %s#%d open: an operation on this page is for that pull request", own, n)
		}
		return cc.entry(ctx, own, n)
	case PageInbox, PagePulls:
		if repository == "" || number < 1 {
			return reviewEntry{}, errors.New("name the pull request: repository and number")
		}
		listed := false
		if c.Page.Page == PageInbox {
			q, err := cc.queue(ctx)
			if err != nil {
				return reviewEntry{}, err
			}
			listed = slices.ContainsFunc(q.Items, func(i queueItem) bool { return i.Key == pullKey(repository, number) })
		} else {
			entries, err := cc.indexEntries(ctx, false)
			if err != nil {
				return reviewEntry{}, err
			}
			listed = slices.ContainsFunc(entries, func(e reviewindex.Entry) bool {
				return e.PullRequest.Number == number && strings.EqualFold(e.PullRequest.Repository, repository)
			})
		}
		if !listed {
			return reviewEntry{}, fmt.Errorf("%s#%d is not on the page the person has open", repository, number)
		}
		return cc.entry(ctx, repository, number)
	}
	return reviewEntry{}, ErrNotCurrentPage
}

// title is the pull request's title as the index has it.
func (e reviewEntry) title() string {
	var pr struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(e.raw["pull_request"], &pr)
	return pr.Title
}

func (e reviewEntry) repository() string {
	var pr struct {
		Repository string `json:"repository"`
	}
	_ = json.Unmarshal(e.raw["pull_request"], &pr)
	return pr.Repository
}

// label names a pull request in baum's sentence: owner/name#N, or "a pull
// request" when the repository is not spelled as GitHub spells one. Its
// title (and such a repository) is a field (fields).
func (e reviewEntry) label() string {
	if !validRepository(e.repository()) {
		return "a pull request"
	}
	return e.repository() + "#" + strconv.Itoa(e.Number)
}

// fields are what the summary quotes of a pull request: its title, and its
// repository and number when the sentence could not name them.
func (e reviewEntry) fields(fields []SummaryField) []SummaryField {
	if !validRepository(e.repository()) {
		fields = quoted(fields, "Pull request", e.repository()+"#"+strconv.Itoa(e.Number), false)
	}
	return quoted(fields, "Pull request title", e.title(), false)
}

// shortSHA is a head as the app shows it.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// pullBody is the body of the pull request writes that take a head: the
// update and rerun-checks routes'.
type pullBody struct {
	Repository string `json:"repository"`
	PR         int    `json:"pr"`
	HeadSHA    string `json:"head_sha"`
}

// bodyPull reads the repository and number of a stored request's body.
func bodyPull(req OperationRequest) (string, int) {
	var b struct {
		Repository string `json:"repository"`
		PR         int    `json:"pr"`
	}
	_ = json.Unmarshal(req.Body, &b)
	return b.Repository, b.PR
}

// ownPullDescribed checks that a pull request write is still for the page's
// pull request, and returns its entry.
func ownPullDescribed(ctx context.Context, c OperationContext, req OperationRequest) (reviewEntry, error) {
	repository, number := bodyPull(req)
	own, n, err := pageOwnPull(c)
	if err != nil {
		return reviewEntry{}, err
	}
	if !strings.EqualFold(repository, own) || number != n {
		return reviewEntry{}, ErrNotCurrentPage
	}
	return c.callContext().entry(ctx, own, n)
}

// options reads one of the pull request's GitHub option routes.
func options(ctx context.Context, c OperationContext, path, repository string, number int, v any) error {
	body, missing, err := c.get(ctx, path, pullQuery(repository, number))
	if err != nil {
		return err
	}
	if missing || disabled(body) {
		return errors.New("this is not available for the pull request now")
	}
	return json.Unmarshal(body, v)
}

// headMatches refuses a head GitHub has that is not the one the page shows,
// as the app's dialogs do.
func headMatches(page, github string) error {
	if github != "" && page != "" && github != page {
		return errors.New("the pull request has new commits since the page loaded: the person refreshes it first")
	}
	return nil
}

// cloneFor is the clone the app reviews or fixes a pull request with: the
// index entry's, else the first available registered clone of its
// repository (app.tsx's cloneFor), and none when the entry needs a clone.
func cloneFor(ctx context.Context, c OperationContext, e reviewEntry) (string, error) {
	errNoClone := errors.New("this needs a local clone: the person chooses a folder or clones the repository in Repositories")
	if e.NeedsClone {
		return "", errNoClone
	}
	if e.CloneID != "" {
		return e.CloneID, nil
	}
	clones, err := registeredClones(ctx, c)
	if err != nil {
		return "", err
	}
	for _, clone := range clones {
		if clone.Available && strings.EqualFold(clone.Repository, e.repository()) {
			return clone.ID, nil
		}
	}
	return "", errNoClone
}

type registeredClone struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Available  bool   `json:"available"`
}

// registeredClones are the clones GET /api/repos lists.
func registeredClones(ctx context.Context, c OperationContext) ([]registeredClone, error) {
	body, _, err := c.get(ctx, "/api/repos", nil)
	if err != nil {
		return nil, errors.New("the registered clones are unavailable now")
	}
	var clones []registeredClone
	if json.Unmarshal(body, &clones) != nil {
		return nil, errors.New("the registered clones are not readable")
	}
	return clones, nil
}

// cloneRepository is the repository of a registered clone, "" when none.
func cloneRepository(ctx context.Context, c OperationContext, id string) (string, error) {
	clones, err := registeredClones(ctx, c)
	if err != nil {
		return "", err
	}
	for _, clone := range clones {
		if clone.ID == id {
			return clone.Repository, nil
		}
	}
	return "", nil
}

// activeJob reports whether a job of the pull request that match picks is
// queued or running.
func activeJob(ctx context.Context, c OperationContext, repository string, number int, match func(kind, group string) bool) (bool, error) {
	body, _, err := c.get(ctx, "/api/jobs", pullQuery(repository, number))
	if err != nil {
		return false, errors.New("the pull request's jobs are unavailable now")
	}
	var list struct {
		Jobs []struct {
			Kind  string `json:"kind"`
			Group string `json:"group"`
			State string `json:"state"`
		} `json:"jobs"`
	}
	if json.Unmarshal(body, &list) != nil {
		return false, errors.New("the pull request's jobs are not readable")
	}
	for _, j := range list.Jobs {
		if (j.State == "queued" || j.State == "running") && match(j.Kind, j.Group) {
			return true, nil
		}
	}
	return false, nil
}

// runChoice is the harness, model and effort a run will use, from the
// settings (a projected read: the person reads it, the agent never does).
//
// A paid run is never proposed, or confirmed, without them: when the
// settings cannot be read, or the route could read only the config file and
// not the app's saved choices, the operation is refused rather than shown
// with a guess the run itself might not use.
func runChoice(ctx context.Context, c OperationContext, fix bool) (backend, model, effort string, err error) {
	settings, err := readSettings(ctx, c.callContext())
	if err != nil || settings.PreferencesUnavailable {
		return "", "", "", errors.New("the harness, model and effort this run would use cannot be read now, so it is not proposed")
	}
	backend, model, effort = settings.Backend, settings.Model, settings.Effort
	if fix && settings.FixBackend != "" {
		backend, model, effort = settings.FixBackend, settings.FixModel, settings.FixEffort
	}
	return setting(backend), orDefault(setting(model)), orDefault(setting(effort)), nil
}

func orDefault(s string) string {
	if s == "" {
		return "the harness's default"
	}
	return s
}

// jobsBody is POST /api/jobs's body, as the app's review and fix send it.
type jobsBody struct {
	Items []jobItem `json:"items"`
	Mode  string    `json:"mode"`
}

type jobItem struct {
	Clone string `json:"clone"`
	PR    int    `json:"pr"`
	Kind  string `json:"kind,omitempty"`
	Only  string `json:"only,omitempty"`
}

// pullTargetSchema is the schema of an operation on one pull request: on a
// pull request's page its own, elsewhere one the page lists.
const pullTargetSchema = `"repository":{"type":"string","maxLength":200,"description":"The pull request's repository as owner/name: on a pull request's page, only to check it is the page's."},` +
	`"number":{"type":"integer","minimum":1,"description":"The pull request's number: on a pull request's page, only to check it is the page's."}`

func pullOperations() []Operation {
	prPage := []string{PagePullRequest}
	return []Operation{{
		Name:         "merge",
		Description:  "Proposes merging the pull request the page shows on GitHub, as the Merge dialog does: with a method GitHub allows (merge, squash or rebase; default the one GitHub preselects), at the head the page shows. The person must have the pull request's page open.",
		Pages:        prPage,
		Method:       http.MethodPost,
		Route:        "/api/prs/merge",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"method":{"type":"string","enum":["MERGE","SQUASH","REBASE"]}},"additionalProperties":false}`),
		Reads:        []string{"/api/index", "/api/prs/merge-options"},
		Irreversible: true,
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				Method string `json:"method"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			e, err := pullOnPage(ctx, c, "", 0)
			if err != nil {
				return OperationRequest{}, err
			}
			var o struct {
				Methods []string `json:"methods"`
				Default string   `json:"default"`
				State   string   `json:"state"`
				Draft   bool     `json:"draft"`
				HeadSHA string   `json:"head_sha"`
			}
			if err := options(ctx, c, "/api/prs/merge-options", e.repository(), e.Number, &o); err != nil {
				return OperationRequest{}, err
			}
			// The Merge dialog's refusals (merge.tsx mergeRefusal).
			switch {
			case o.State != "OPEN":
				return OperationRequest{}, errors.New("the pull request is not open")
			case o.Draft:
				return OperationRequest{}, errors.New("the pull request is a draft")
			case len(o.Methods) == 0:
				return OperationRequest{}, errors.New("GitHub allows no merge method for this pull request")
			}
			if err := headMatches(e.HeadSHA, o.HeadSHA); err != nil {
				return OperationRequest{}, err
			}
			method := a.Method
			if method == "" {
				method = o.Default
				if !slices.Contains(o.Methods, method) {
					method = o.Methods[0]
				}
			}
			if !slices.Contains(o.Methods, method) {
				return OperationRequest{}, fmt.Errorf("GitHub allows %s for this pull request", strings.Join(o.Methods, ", "))
			}
			if !slices.Contains([]string{"MERGE", "SQUASH", "REBASE"}, method) {
				return OperationRequest{}, errors.New("GitHub offers a merge method baum does not know: the person merges from the app")
			}
			body, err := json.Marshal(struct {
				Repository string `json:"repository"`
				PR         int    `json:"pr"`
				Method     string `json:"method"`
				HeadSHA    string `json:"head_sha"`
			}{e.repository(), e.Number, method, e.HeadSHA})
			return OperationRequest{Target: pullKey(e.repository(), e.Number), Body: body}, err
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			e, err := ownPullDescribed(ctx, c, req)
			if err != nil {
				return OperationSummary{}, err
			}
			var b struct {
				Method  string `json:"method"`
				HeadSHA string `json:"head_sha"`
			}
			_ = json.Unmarshal(req.Body, &b)
			return OperationSummary{Action: "Merge " + e.label() + " on GitHub",
				Details: e.fields(headField([]SummaryField{fact("Method", strings.ToLower(b.Method))}, b.HeadSHA))}, nil
		},
	}, pullWrite("update_branch", "/api/prs/update-branch", "/api/prs/update-branch-options",
		"Proposes updating the pull request's branch with its base on GitHub, as the Update branch dialog does, at the head the page shows. The person must have the pull request's page open.",
		func(body json.RawMessage, head string) error {
			var o struct {
				State    string `json:"state"`
				HeadSHA  string `json:"head_sha"`
				BaseRef  string `json:"base_ref"`
				Conflict bool   `json:"conflict"`
				Fork     bool   `json:"fork"`
			}
			if json.Unmarshal(body, &o) != nil {
				return errors.New("the branch's state is not readable")
			}
			switch {
			case o.State != "" && o.State != "OPEN":
				return errors.New("the pull request is not open")
			case o.Fork:
				return errors.New("the pull request is from a fork: baum cannot update its branch")
			case o.Conflict:
				return errors.New("the branch conflicts with its base: GitHub cannot update it")
			}
			return headMatches(head, o.HeadSHA)
		},
		func(e reviewEntry) string { return "Update the branch of " + e.label() + " with its base on GitHub" }),
		pullWrite("rerun_checks", "/api/prs/rerun-checks", "/api/prs/rerun-checks-options",
			"Proposes re-running the failed GitHub Actions runs of the pull request's head, as the Re-run failed checks dialog does. The person must have the pull request's page open.",
			func(body json.RawMessage, head string) error {
				var o struct {
					State   string            `json:"state"`
					HeadSHA string            `json:"head_sha"`
					Runs    []json.RawMessage `json:"runs"`
				}
				if json.Unmarshal(body, &o) != nil {
					return errors.New("the checks are not readable")
				}
				if len(o.Runs) == 0 {
					return errors.New("no failed workflow run can be re-run")
				}
				return headMatches(head, o.HeadSHA)
			},
			func(e reviewEntry) string { return "Re-run the failed checks of " + e.label() + " on GitHub" }),
		{
			Name:        "rerun_failed_job",
			Description: "Proposes re-running one failed GitHub Actions job of the pull request's head, as the Checks list's Re-run does: job_id is the check's job_id (see checks). The person must have the pull request's page open.",
			Pages:       prPage,
			Method:      http.MethodPost,
			Route:       "/api/prs/rerun-job",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"integer","minimum":1}},"required":["job_id"],"additionalProperties":false}`),
			Reads:       []string{"/api/index", "/api/prs/failing-checks"},
			Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
				var a struct {
					JobID int64 `json:"job_id"`
				}
				if err := strictArgs(args, &a); err != nil {
					return OperationRequest{}, err
				}
				e, err := pullOnPage(ctx, c, "", 0)
				if err != nil {
					return OperationRequest{}, err
				}
				body, err := json.Marshal(struct {
					Repository string `json:"repository"`
					PR         int    `json:"pr"`
					HeadSHA    string `json:"head_sha"`
					JobID      int64  `json:"job_id"`
				}{e.repository(), e.Number, e.HeadSHA, a.JobID})
				return OperationRequest{Target: pullKey(e.repository(), e.Number), Body: body}, err
			},
			Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
				e, err := ownPullDescribed(ctx, c, req)
				if err != nil {
					return OperationSummary{}, err
				}
				var b struct {
					HeadSHA string `json:"head_sha"`
					JobID   int64  `json:"job_id"`
				}
				_ = json.Unmarshal(req.Body, &b)
				query := pullQuery(e.repository(), e.Number)
				query.Set("head_sha", b.HeadSHA)
				body, _, err := c.get(ctx, "/api/prs/failing-checks", query)
				if err != nil {
					return OperationSummary{}, err
				}
				var checks struct {
					Checks []failingCheck `json:"checks"`
				}
				_ = json.Unmarshal(body, &checks)
				i := slices.IndexFunc(checks.Checks, func(check failingCheck) bool { return check.JobID == b.JobID })
				if i < 0 {
					return OperationSummary{}, errors.New("that job is not one of the pull request's failing checks")
				}
				return OperationSummary{Action: "Re-run one failed check of " + e.label() + " on GitHub",
					Details: e.fields(quoted([]SummaryField{fact("GitHub Actions job", strconv.FormatInt(b.JobID, 10))}, "Check", checks.Checks[i].Name, false))}, nil
			},
		}, {
			Name:        "resolve_thread",
			Description: "Proposes resolving one unresolved review thread of the pull request on GitHub, as the Threads list's Resolve does: thread is the thread's id (see review_threads). The person must have the pull request's page open.",
			Pages:       prPage,
			Method:      http.MethodPost,
			Route:       "/api/prs/resolve-thread",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"thread":{"type":"string","minLength":1,"maxLength":200}},"required":["thread"],"additionalProperties":false}`),
			Reads:       []string{"/api/index", "/api/prs/review-threads"},
			Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
				var a struct {
					Thread string `json:"thread"`
				}
				if err := strictArgs(args, &a); err != nil {
					return OperationRequest{}, err
				}
				e, err := pullOnPage(ctx, c, "", 0)
				if err != nil {
					return OperationRequest{}, err
				}
				body, err := json.Marshal(struct {
					Repository string `json:"repository"`
					PR         int    `json:"pr"`
					Thread     string `json:"thread"`
				}{e.repository(), e.Number, a.Thread})
				return OperationRequest{Target: pullKey(e.repository(), e.Number), Body: body}, err
			},
			Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
				e, err := ownPullDescribed(ctx, c, req)
				if err != nil {
					return OperationSummary{}, err
				}
				var b struct {
					Thread string `json:"thread"`
				}
				_ = json.Unmarshal(req.Body, &b)
				query := pullQuery(e.repository(), e.Number)
				if e.Unresolved != nil {
					query.Set("unresolved", strconv.Itoa(*e.Unresolved))
				}
				body, _, err := c.get(ctx, "/api/prs/review-threads", query)
				if err != nil {
					return OperationSummary{}, err
				}
				var threads struct {
					Threads []struct {
						ID         string `json:"id"`
						Path       string `json:"path"`
						Line       int    `json:"line"`
						Author     string `json:"author"`
						Excerpt    string `json:"excerpt"`
						CanResolve bool   `json:"can_resolve"`
					} `json:"threads"`
				}
				_ = json.Unmarshal(body, &threads)
				for _, t := range threads.Threads {
					if t.ID != b.Thread {
						continue
					}
					if !t.CanResolve {
						return OperationSummary{}, errors.New("the signed-in account cannot resolve that thread")
					}
					where := t.Path
					if t.Line > 0 {
						where += ":" + strconv.Itoa(t.Line)
					}
					fields := e.fields(nil)
					fields = quoted(fields, "On", where, false)
					fields = quoted(fields, "By", t.Author, false)
					fields = quoted(fields, "Thread", t.Excerpt, true)
					return OperationSummary{Action: "Resolve a review thread of " + e.label() + " on GitHub", Details: fields}, nil
				}
				return OperationSummary{}, errors.New("that thread is not one of the pull request's unresolved threads")
			},
		}, {
			Name:        "fix",
			Description: "Proposes starting a fix of the pull request the page shows, as its Fix button does: an agent run that pushes to the pull request's branch and answers its review threads; only=checks fixes the failing checks alone (Fix CI). The person must have the pull request's page open.",
			Pages:       prPage,
			Method:      http.MethodPost,
			Route:       "/api/jobs",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"only":{"type":"string","enum":["checks"]}},"additionalProperties":false}`),
			Reads:       []string{"/api/index", "/api/repos", "/api/jobs"},
			Projected:   []string{"/api/settings"},
			Paid:        true,
			Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
				var a struct {
					Only string `json:"only"`
				}
				if err := strictArgs(args, &a); err != nil {
					return OperationRequest{}, err
				}
				e, err := pullOnPage(ctx, c, "", 0)
				if err != nil {
					return OperationRequest{}, err
				}
				clone, err := cloneFor(ctx, c, e)
				if err != nil {
					return OperationRequest{}, err
				}
				if running, err := activeJob(ctx, c, e.repository(), e.Number, func(kind, _ string) bool { return kind == "fix" }); err != nil {
					return OperationRequest{}, err
				} else if running {
					return OperationRequest{}, fmt.Errorf("a fix: %w", errAlreadyRunning)
				}
				body, err := json.Marshal(jobsBody{Items: []jobItem{{Clone: clone, PR: e.Number, Kind: "fix", Only: a.Only}}, Mode: "single"})
				return OperationRequest{Target: pullKey(e.repository(), e.Number), Body: body}, err
			},
			Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
				e, item, err := jobPull(ctx, c, req)
				if err != nil {
					return OperationSummary{}, err
				}
				backend, model, effort, err := runChoice(ctx, c, true)
				if err != nil {
					return OperationSummary{}, err
				}
				action := "Start a fix of " + e.label()
				details := []SummaryField{fact("What runs", "An agent pushes to the pull request's branch and answers its review threads")}
				if item.Only == "checks" {
					action = "Start a fix of the failing checks of " + e.label()
					details = []SummaryField{fact("What runs", "An agent pushes to the pull request's branch")}
				}
				if backend != "" {
					details = append(details, fact("Harness", backend))
				}
				return OperationSummary{Action: action, Details: e.fields(details), Model: model, Effort: effort}, nil
			},
		}, {
			Name:        "review",
			Description: "Proposes starting a review of a pull request, as Generate review (or Regenerate review) does: on a pull request's page that pull request, on the Inbox or Pull requests a pull request the page lists. A pull request from a fork or an untrusted author needs the person's isolation confirmation in the app, which this does not give: the route refuses it and the person starts it from the app.",
			Pages:       []string{PagePullRequest, PageInbox, PagePulls},
			Method:      http.MethodPost,
			Route:       "/api/jobs",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` + pullTargetSchema + `},"additionalProperties":false}`),
			Reads:       []string{"/api/index", "/api/inbox", "/api/repos", "/api/jobs"},
			Projected:   []string{"/api/settings"},
			Paid:        true,
			Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
				var a struct {
					Repository string `json:"repository"`
					Number     int    `json:"number"`
				}
				if err := strictArgs(args, &a); err != nil {
					return OperationRequest{}, err
				}
				e, err := pullOnPage(ctx, c, a.Repository, a.Number)
				if err != nil {
					return OperationRequest{}, err
				}
				clone, err := cloneFor(ctx, c, e)
				if err != nil {
					return OperationRequest{}, err
				}
				if running, err := activeJob(ctx, c, e.repository(), e.Number, func(kind, group string) bool { return kind == "" && group == "" }); err != nil {
					return OperationRequest{}, err
				} else if running {
					return OperationRequest{}, fmt.Errorf("a review: %w", errAlreadyRunning)
				}
				body, err := json.Marshal(jobsBody{Items: []jobItem{{Clone: clone, PR: e.Number}}, Mode: "single"})
				return OperationRequest{Target: pullKey(e.repository(), e.Number), Body: body}, err
			},
			Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
				e, _, err := jobPull(ctx, c, req)
				if err != nil {
					return OperationSummary{}, err
				}
				backend, model, effort, err := runChoice(ctx, c, false)
				if err != nil {
					return OperationSummary{}, err
				}
				action := "Start a review of " + e.label()
				if e.VersionCount > 0 {
					action = "Start a new review of " + e.label()
				}
				details := []SummaryField{fact("What runs", "An agent reviews the pull request")}
				if backend != "" {
					details = append(details, fact("Harness", backend))
				}
				return OperationSummary{Action: action, Details: e.fields(details), Model: model, Effort: effort}, nil
			},
		}, {
			Name:        "refresh",
			Description: "Proposes reading a repository's pull requests from GitHub now rather than at the next poll, as Refresh does for each repository. repository is one baum watches (see repositories).",
			Pages:       []string{PageInbox, PagePulls},
			Method:      http.MethodPost,
			Route:       "/api/prs/refresh",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","minLength":3,"maxLength":200}},"required":["repository"],"additionalProperties":false}`),
			Reads:       []string{"/api/repositories", "/api/repos"},
			Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
				var a struct {
					Repository string `json:"repository"`
				}
				if err := strictArgs(args, &a); err != nil {
					return OperationRequest{}, err
				}
				// As the app does: by the repository when it is picked, else by
				// a registered clone of it. What is sent, and named, is the
				// registry's own spelling, not the agent's.
				repository := strings.TrimSpace(a.Repository)
				picked, err := pickedRepositories(ctx, c)
				if err != nil {
					return OperationRequest{}, err
				}
				var body []byte
				if i := slices.IndexFunc(picked, func(r pickedRepository) bool { return strings.EqualFold(r.Repository, repository) }); i >= 0 {
					repository = picked[i].Repository
					body, err = json.Marshal(struct {
						Repository string `json:"repository"`
					}{repository})
				} else {
					clones, cerr := registeredClones(ctx, c)
					if cerr != nil {
						return OperationRequest{}, cerr
					}
					i := slices.IndexFunc(clones, func(r registeredClone) bool { return strings.EqualFold(r.Repository, repository) })
					if i < 0 {
						return OperationRequest{}, fmt.Errorf("%s is not a repository baum watches", a.Repository)
					}
					repository = clones[i].Repository
					body, err = json.Marshal(struct {
						Clone string `json:"clone"`
					}{clones[i].ID})
				}
				if !validRepository(repository) {
					return OperationRequest{}, errors.New("that repository's name is not one baum can name: the person refreshes it from the app")
				}
				return OperationRequest{Target: repository, Body: body}, err
			},
			Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
				var b struct {
					Repository string `json:"repository"`
					Clone      string `json:"clone"`
				}
				_ = json.Unmarshal(req.Body, &b)
				repository := b.Repository
				if b.Clone != "" {
					var err error
					if repository, err = cloneRepository(ctx, c, b.Clone); err != nil {
						return OperationSummary{}, err
					}
				}
				if repository == "" || repository != req.Target {
					return OperationSummary{}, errors.New("the repository is no longer registered")
				}
				return OperationSummary{Action: "Read the pull requests of " + req.Target + " from GitHub now"}, nil
			},
		}}
}

// failingCheck is a check of GET /api/prs/failing-checks.
type failingCheck struct {
	Name  string `json:"name"`
	JobID int64  `json:"job_id"`
}

// pullWrite is an operation that sends {repository, pr, head_sha} for the
// page's pull request to route, after its dialog's options (optionsRoute)
// pass check.
func pullWrite(name, route, optionsRoute, description string, check func(options json.RawMessage, head string) error, action func(reviewEntry) string) Operation {
	return Operation{
		Name:        name,
		Description: description,
		Pages:       []string{PagePullRequest},
		Method:      http.MethodPost,
		Route:       route,
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Reads:       []string{"/api/index", optionsRoute},
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			if err := strictArgs(args, &struct{}{}); err != nil {
				return OperationRequest{}, err
			}
			e, err := pullOnPage(ctx, c, "", 0)
			if err != nil {
				return OperationRequest{}, err
			}
			var o json.RawMessage
			if err := options(ctx, c, optionsRoute, e.repository(), e.Number, &o); err != nil {
				return OperationRequest{}, err
			}
			if err := check(o, e.HeadSHA); err != nil {
				return OperationRequest{}, err
			}
			body, err := json.Marshal(pullBody{e.repository(), e.Number, e.HeadSHA})
			return OperationRequest{Target: pullKey(e.repository(), e.Number), Body: body}, err
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			e, err := ownPullDescribed(ctx, c, req)
			if err != nil {
				return OperationSummary{}, err
			}
			var b pullBody
			_ = json.Unmarshal(req.Body, &b)
			return OperationSummary{Action: action(e), Details: e.fields(headField(nil, b.HeadSHA))}, nil
		},
	}
}

// jobPull checks that a job request is still for a pull request the page
// shows, through a clone of its repository, and returns its entry and item.
func jobPull(ctx context.Context, c OperationContext, req OperationRequest) (reviewEntry, jobItem, error) {
	var b jobsBody
	if json.Unmarshal(req.Body, &b) != nil || len(b.Items) != 1 {
		return reviewEntry{}, jobItem{}, ErrNotCurrentPage
	}
	repository, number, ok := parsePullKey(req.Target)
	if !ok || b.Items[0].PR != number {
		return reviewEntry{}, jobItem{}, ErrNotCurrentPage
	}
	e, err := pullOnPage(ctx, c, repository, number)
	if err != nil {
		return reviewEntry{}, jobItem{}, err
	}
	if b.Items[0].Clone != e.CloneID {
		owner, err := cloneRepository(ctx, c, b.Items[0].Clone)
		if err != nil {
			return reviewEntry{}, jobItem{}, err
		}
		if !strings.EqualFold(owner, repository) {
			return reviewEntry{}, jobItem{}, errors.New("the clone is no longer registered")
		}
	}
	return e, b.Items[0], nil
}

// pickedRepository is a row of GET /api/repositories.
type pickedRepository struct {
	Repository string            `json:"repository"`
	Clones     []registeredClone `json:"clones"`
}

func pickedRepositories(ctx context.Context, c OperationContext) ([]pickedRepository, error) {
	body, _, err := c.get(ctx, "/api/repositories", nil)
	if err != nil {
		return nil, errors.New("the registered repositories are unavailable now")
	}
	var list []pickedRepository
	if json.Unmarshal(body, &list) != nil {
		return nil, errors.New("the registered repositories are not readable")
	}
	return list, nil
}

// liveSession is a row of GET /api/live-sessions.
type liveSession struct {
	Agent       string `json:"agent"`
	SessionID   string `json:"session_id"`
	State       string `json:"state"`
	Title       string `json:"title"`
	Repository  string `json:"repository"`
	Endable     bool   `json:"endable"`
	EndBlocked  string `json:"end_blocked"`
	Messageable bool   `json:"messageable"`
	Focus       string `json:"focus"`
}

func (s liveSession) key() string { return SessionKey(s.Agent, s.SessionID) }

// label names a session in baum's sentence: its agent and id, or "an agent
// session" when either is not one plain segment. Its title and repository
// (and such an agent or id) are fields (fields).
func (s liveSession) label() string {
	if !s.nameable() {
		return "an agent session"
	}
	return s.Agent + " session " + s.SessionID
}

func (s liveSession) nameable() bool { return validSegment(s.Agent) && validSegment(s.SessionID) }

// fields are what the summary quotes of a session: its title and
// repository, and its agent and id when the sentence could not name them.
func (s liveSession) fields(fields []SummaryField) []SummaryField {
	if !s.nameable() {
		fields = quoted(fields, "Agent", s.Agent, false)
		fields = quoted(fields, "Session id", s.SessionID, false)
	}
	fields = quoted(fields, "Session title", s.Title, false)
	return quoted(fields, "Repository", s.Repository, false)
}

// errOwnSession is an operation on the chat's own session.
var errOwnSession = errors.New("a chat cannot message, end or bring forward its own session: the person does that in the app")

// sessionOnPage finds the session an operation acts on: a session's page
// shows its own (one named must be it), the Sessions page any it lists. The
// chat's own session is refused, and every session while the chat's own is
// not known: it could be its own.
func sessionOnPage(ctx context.Context, c OperationContext, named string) (liveSession, error) {
	if c.Consumer.Session == "" {
		return liveSession{}, errNoSession
	}
	s, err := listedSession(ctx, c, named)
	if err != nil {
		return liveSession{}, err
	}
	if s.key() == c.Consumer.Session {
		return liveSession{}, errOwnSession
	}
	return s, nil
}

// listedSession is sessionOnPage without the own-session check.
func listedSession(ctx context.Context, c OperationContext, named string) (liveSession, error) {
	named = strings.TrimSpace(named)
	switch c.Page.Page {
	case PageSession:
		if c.Page.Session == "" {
			return liveSession{}, ErrNotCurrentPage
		}
		if named != "" && named != c.Page.Session {
			return liveSession{}, fmt.Errorf("the person has session %s open: an operation on this page is for that session", c.Page.Session)
		}
		named = c.Page.Session
	case PageSessions:
		if named == "" {
			return liveSession{}, errors.New("name the session as agent:id")
		}
	default:
		return liveSession{}, ErrNotCurrentPage
	}
	body, _, err := c.get(ctx, "/api/live-sessions", nil)
	if err != nil {
		return liveSession{}, errors.New("the sessions are unavailable now")
	}
	var list struct {
		Sessions []liveSession `json:"sessions"`
	}
	if disabled(body) || json.Unmarshal(body, &list) != nil {
		return liveSession{}, errors.New("the sessions are unavailable now")
	}
	for _, s := range list.Sessions {
		if s.key() == named {
			return s, nil
		}
	}
	return liveSession{}, fmt.Errorf("session %s is not on the page the person has open", named)
}

// sessionVars are a live session route's {agent} and {id}.
func sessionVars(s liveSession) map[string]string {
	return map[string]string{"agent": s.Agent, "id": s.SessionID}
}

// sessionDescribed checks that a live session route's session is still on
// the page.
func sessionDescribed(ctx context.Context, c OperationContext, req OperationRequest) (liveSession, error) {
	s, err := sessionOnPage(ctx, c, SessionKey(req.Vars["agent"], req.Vars["id"]))
	if err != nil {
		return liveSession{}, err
	}
	if s.key() != req.Target {
		return liveSession{}, ErrNotCurrentPage
	}
	return s, nil
}

// agentRequest is a pending request of GET /api/agent-requests.
type agentRequest struct {
	ID        string `json:"id"`
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	Tool      string `json:"tool"`
	Input     []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"input"`
	Questions []requestQuestion `json:"questions"`
	// Altered is a request the app does not show exactly as the agent sent
	// it (agentrequests.Request.Altered).
	Altered bool `json:"altered"`
}

// errAltered refuses allowing a request the app shows cut, cleaned or
// masked: the person would allow what they did not see.
var errAltered = errors.New("the app shows this request cut, cleaned or with a secret masked, so it cannot be allowed from a proposal: the person answers it in the app or the terminal (denying it may be proposed)")

// exact reports whether the summary shows the request exactly as the agent
// sent it: the store neither cut, cleaned nor masked it (Altered), and
// nothing in it is text plain would change.
func (r agentRequest) exact() bool {
	same := func(text string, multiline bool) bool { return plain(text, multiline) == text }
	if r.Altered || !same(r.Tool, false) {
		return false
	}
	for _, row := range r.Input {
		if !same(row.Label, false) || !same(row.Value, true) {
			return false
		}
	}
	for _, q := range r.Questions {
		if !same(q.Question, true) {
			return false
		}
	}
	return true
}

type requestQuestion struct {
	Question string `json:"question"`
}

// requestOnPage finds a pending request the open page lists: the Sessions
// page lists every session's, a session's page its own. A request of the
// chat's own session is refused, and so is every request when the chat's
// session is not known: it could be its own.
func requestOnPage(ctx context.Context, c OperationContext, id string) (agentRequest, error) {
	if c.Page.Page != PageSessions && (c.Page.Page != PageSession || c.Page.Session == "") {
		return agentRequest{}, ErrNotCurrentPage
	}
	if c.Consumer.Session == "" {
		return agentRequest{}, errNoSession
	}
	requests, _, err := c.callContext().agentRequestsBody(ctx)
	if err != nil {
		return agentRequest{}, err
	}
	var list []agentRequest
	if json.Unmarshal(requests, &list) != nil {
		return agentRequest{}, errors.New("the agent requests are not readable")
	}
	for _, r := range list {
		if r.ID != id {
			continue
		}
		key := SessionKey(r.Agent, r.SessionID)
		if key == c.Consumer.Session {
			return agentRequest{}, errOwnRequest
		}
		if c.Page.Page == PageSession && key != c.Page.Session {
			return agentRequest{}, fmt.Errorf("that request is another session's: the person has session %s open", c.Page.Session)
		}
		return r, nil
	}
	return agentRequest{}, errors.New("that request is not pending on the page the person has open")
}

// answerBody is POST /api/agent-requests/{id}/answer's body, without the
// route's suggestion: a permission suggestion can change the agent's
// permissions for good (a mode, a rule in the user's settings), which only
// the person applies, in the app.
type answerBody struct {
	Behavior string            `json:"behavior"`
	Answers  map[string]string `json:"answers,omitempty"`
}

// requestSession is the summary's fields of the session a request is of:
// its title and repository when the session is listed.
func requestSession(ctx context.Context, c OperationContext, r agentRequest) []SummaryField {
	body, _, err := c.get(ctx, "/api/live-sessions", nil)
	var list struct {
		Sessions []liveSession `json:"sessions"`
	}
	if err != nil || disabled(body) || json.Unmarshal(body, &list) != nil {
		list.Sessions = nil
	}
	for _, s := range list.Sessions {
		if s.key() == SessionKey(r.Agent, r.SessionID) {
			return s.fields(nil)
		}
	}
	return liveSession{Agent: r.Agent, SessionID: r.SessionID}.fields(nil)
}

func sessionOperations() []Operation {
	sessionPages := []string{PageSessions, PageSession}
	sessionArg := `"session":{"type":"string","maxLength":600,"description":"The session as agent:id: on a session's page, only to check it is the page's."}`
	return []Operation{{
		Name: "answer_request",
		Description: "Proposes answering an agent's pending request (see agent_requests), as the Sessions pages do: allow or deny a permission request, or answer a question (answers keyed by each question's text). A permission's suggestions (allow always, a mode) are the person's to apply in the app: you cannot propose one. Only a request the open page lists: the Sessions page lists every session's, a session's page its own. " +
			"A request the app shows cut, cleaned or with a secret masked (altered) can only be denied. " +
			"You can never answer a request of your own session, nor any when the app has not told baum which session you run in.",
		Pages:  sessionPages,
		Method: http.MethodPost,
		Route:  "/api/agent-requests/{id}/answer",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"id":{"type":"string","minLength":1,"maxLength":200},` +
			`"behavior":{"type":"string","enum":["allow","deny"]},` +
			`"answers":{"type":"object","maxProperties":16,"additionalProperties":{"type":"string","minLength":1,"maxLength":1000}}},` +
			`"required":["id","behavior"],"additionalProperties":false}`),
		Reads: []string{"/api/agent-requests", "/api/live-sessions"},
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				ID string `json:"id"`
				answerBody
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			r, err := requestOnPage(ctx, c, a.ID)
			if err != nil {
				return OperationRequest{}, err
			}
			if len(a.Answers) > 0 && (a.Behavior != "allow" || r.Kind != "question") {
				return OperationRequest{}, errors.New("answers go only with allow, to a question")
			}
			if a.Behavior == "allow" && !r.exact() {
				return OperationRequest{}, errAltered
			}
			for question, answer := range a.Answers {
				if !slices.ContainsFunc(r.Questions, func(q requestQuestion) bool { return q.Question == question }) {
					return OperationRequest{}, errors.New("the answers name a question the request does not ask")
				}
				if err := checkAgentText("answer", answer, true); err != nil {
					return OperationRequest{}, err
				}
			}
			body, err := json.Marshal(a.answerBody)
			return OperationRequest{Target: "request:" + r.ID, Vars: map[string]string{"id": r.ID}, Body: body}, err
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			r, err := requestOnPage(ctx, c, req.Vars["id"])
			if err != nil {
				return OperationSummary{}, err
			}
			var b answerBody
			_ = json.Unmarshal(req.Body, &b)
			if b.Behavior == "allow" && !r.exact() {
				return OperationSummary{}, errAltered
			}
			who := liveSession{Agent: r.Agent, SessionID: r.SessionID}.label()
			s := OperationSummary{Details: requestSession(ctx, c, r)}
			if r.Kind == "question" {
				s.Action = "Answer the questions " + who + " asks"
				if b.Behavior == "deny" {
					s.Action = "Decline the questions " + who + " asks"
				}
				// Every question in full, each with the answer the agent
				// wrote, exactly as it is sent.
				for _, q := range r.Questions {
					s.Details = quoted(s.Details, "Question", q.Question, true)
					if answer, ok := b.Answers[q.Question]; ok {
						s.Details = append(s.Details, agentWrote("Answer", answer, true))
					}
				}
			} else {
				s.Action = "Allow the tool use " + who + " asks for"
				if b.Behavior == "deny" {
					s.Action = "Deny the tool use " + who + " asks for"
				}
				s.Details = quoted(s.Details, "Tool", r.Tool, false)
				// Every input value in full: the store bounds them.
				for _, row := range r.Input {
					s.Details = quoted(s.Details, "Input name", row.Label, false)
					s.Details = quoted(s.Details, "Input value", row.Value, true)
				}
			}
			return s, nil
		},
	}, {
		Name:        "send_message",
		Description: "Proposes sending a message to an agent session that waits at its prompt (messageable), as the Sessions pages' Reply does: it reaches the session as a new turn in its own terminal. On the Sessions page name the session; on a session's page it is the page's.",
		Pages:       sessionPages,
		Method:      http.MethodPost,
		Route:       "/api/live-sessions/{agent}/{id}/message",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + sessionArg + `,"text":{"type":"string","minLength":1,"maxLength":10000}},"required":["text"],"additionalProperties":false}`),
		Reads:       []string{"/api/live-sessions"},
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				Session string `json:"session"`
				Text    string `json:"text"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			s, err := sessionOnPage(ctx, c, a.Session)
			if err != nil {
				return OperationRequest{}, err
			}
			if !s.Messageable {
				return OperationRequest{}, errors.New("that session is not waiting at its prompt: a running one takes a queued message (queue_message)")
			}
			if err := checkAgentText("message", a.Text, true); err != nil {
				return OperationRequest{}, err
			}
			body, err := json.Marshal(struct {
				Text string `json:"text"`
			}{a.Text})
			return OperationRequest{Target: s.key(), Vars: sessionVars(s), Body: body}, err
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			s, err := sessionDescribed(ctx, c, req)
			if err != nil {
				return OperationSummary{}, err
			}
			var b struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(req.Body, &b)
			return OperationSummary{Action: "Send a message to " + s.label(), Details: append(s.fields(nil), agentWrote("Message", b.Text, true))}, nil
		},
	}, {
		Name:        "queue_message",
		Description: "Proposes queuing a message for a running agent session, as the Sessions pages do while it works: the session gets it when its turn ends, and a newer message replaces it. On the Sessions page name the session; on a session's page it is the page's.",
		Pages:       sessionPages,
		Method:      http.MethodPost,
		Route:       "/api/agent-messages",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + sessionArg + `,"text":{"type":"string","minLength":1,"maxLength":4000}},"required":["text"],"additionalProperties":false}`),
		Reads:       []string{"/api/live-sessions", "/api/agent-requests"},
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				Session string `json:"session"`
				Text    string `json:"text"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			s, err := sessionOnPage(ctx, c, a.Session)
			if err != nil {
				return OperationRequest{}, err
			}
			if s.State != "running" {
				return OperationRequest{}, errors.New("that session is not running: one waiting at its prompt takes a message (send_message)")
			}
			if err := checkAgentText("message", a.Text, true); err != nil {
				return OperationRequest{}, err
			}
			body, err := json.Marshal(struct {
				Agent     string `json:"agent"`
				SessionID string `json:"session_id"`
				Text      string `json:"text"`
			}{s.Agent, s.SessionID, a.Text})
			return OperationRequest{Target: s.key(), Body: body}, err
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			var b struct {
				Agent     string `json:"agent"`
				SessionID string `json:"session_id"`
				Text      string `json:"text"`
			}
			_ = json.Unmarshal(req.Body, &b)
			s, err := sessionOnPage(ctx, c, SessionKey(b.Agent, b.SessionID))
			if err != nil {
				return OperationSummary{}, err
			}
			if s.key() != req.Target {
				return OperationSummary{}, ErrNotCurrentPage
			}
			details := append(s.fields(nil), agentWrote("Message", b.Text, true))
			// A session keeps one queued message: say which one this replaces.
			if _, messages, err := c.callContext().agentRequestsBody(ctx); err == nil {
				var queued []struct {
					Agent     string `json:"agent"`
					SessionID string `json:"session_id"`
					Text      string `json:"text"`
				}
				_ = json.Unmarshal(messages, &queued)
				for _, m := range queued {
					if SessionKey(m.Agent, m.SessionID) == s.key() {
						details = append(details, fact("Replaces", "The message already queued for this session, which is dropped"))
						details = quoted(details, "Queued message", m.Text, true)
					}
				}
			}
			return OperationSummary{Action: "Queue a message for " + s.label() + ", for when its turn ends", Details: details}, nil
		},
	}, sessionAction("end_session", "end", true,
		"Proposes ending an agent session that waits on the person or went quiet (endable), as the Sessions pages' End session does: baum stops its agent. On the Sessions page name the session; on a session's page it is the page's.",
		func(s liveSession) error {
			if !s.Endable {
				if s.EndBlocked == "running_command" {
					return errors.New("that session is still running a command")
				}
				return errors.New("baum cannot end that session now")
			}
			return nil
		}, func(s liveSession) string { return "End " + s.label() + " (baum stops its agent)" }),
		sessionAction("focus_session", "focus", false,
			"Proposes bringing an agent session's terminal forward, as the Sessions pages' Focus does. On the Sessions page name the session; on a session's page it is the page's.",
			func(s liveSession) error {
				if s.Focus == "" {
					return errors.New("baum cannot bring that session forward")
				}
				return nil
			}, func(s liveSession) string { return "Bring " + s.label() + " forward" }),
	}
}

// sessionAction is an operation that posts {} to a live session's verb
// route, when check allows it for the session.
func sessionAction(name, verb string, irreversible bool, description string, check func(liveSession) error, action func(liveSession) string) Operation {
	return Operation{
		Name:         name,
		Description:  description,
		Pages:        []string{PageSessions, PageSession},
		Method:       http.MethodPost,
		Route:        "/api/live-sessions/{agent}/{id}/" + verb,
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"session":{"type":"string","maxLength":600,"description":"The session as agent:id: on a session's page, only to check it is the page's."}},"additionalProperties":false}`),
		Reads:        []string{"/api/live-sessions"},
		Irreversible: irreversible,
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				Session string `json:"session"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			s, err := sessionOnPage(ctx, c, a.Session)
			if err != nil {
				return OperationRequest{}, err
			}
			if err := check(s); err != nil {
				return OperationRequest{}, err
			}
			return OperationRequest{Target: s.key(), Vars: sessionVars(s), Body: json.RawMessage(`{}`)}, nil
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			s, err := sessionDescribed(ctx, c, req)
			if err != nil {
				return OperationSummary{}, err
			}
			return OperationSummary{Action: action(s), Details: s.fields(nil)}, nil
		},
	}
}

// jobOnPage reads a job the open page shows: a job's page its own (one
// named must be it), the Jobs page one of the jobs it lists as queued or
// running (GET /api/jobs, the jobs the app keeps current).
func jobOnPage(ctx context.Context, c OperationContext, id string) (map[string]json.RawMessage, error) {
	id = strings.TrimSpace(id)
	switch c.Page.Page {
	case PageJob:
		if c.Page.Job == "" || (id != "" && id != c.Page.Job) {
			return nil, ErrNotCurrentPage
		}
		id = c.Page.Job
	case PageJobs:
		if id == "" {
			return nil, errors.New("name the job by its id")
		}
		body, _, err := c.get(ctx, "/api/jobs", nil)
		if err != nil {
			return nil, errors.New("the jobs are unavailable now")
		}
		var list struct {
			Jobs []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"jobs"`
		}
		if json.Unmarshal(body, &list) != nil {
			return nil, errors.New("the jobs are not readable")
		}
		if !slices.ContainsFunc(list.Jobs, func(j struct {
			ID    string `json:"id"`
			State string `json:"state"`
		}) bool {
			return j.ID == id && (j.State == "queued" || j.State == "running")
		}) {
			return nil, fmt.Errorf("job %s is not one the Jobs page lists as queued or running", id)
		}
	default:
		return nil, ErrNotCurrentPage
	}
	if !validSegment(id) {
		return nil, errors.New("not a job id")
	}
	body, missing, err := c.get(ctx, "/api/jobs/"+id, nil)
	if err != nil {
		return nil, errors.New("the job is unavailable now")
	}
	var job map[string]json.RawMessage
	if missing || json.Unmarshal(body, &job) != nil {
		return nil, fmt.Errorf("job %s is not on the page the person has open", id)
	}
	return job, nil
}

func jobField(job map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(job[key], &s)
	return s
}

// jobLabel names a job in baum's sentence: what it does (in baum's words),
// its id, and its repository and pull request, each only when it passes its
// check; what does not is quoted in the fields.
func jobLabel(job map[string]json.RawMessage) (string, []SummaryField) {
	var fields []SummaryField
	kind := jobField(job, "kind")
	what, known := map[string]string{"": "review", "fix": "fix", "details": "details retry", "feature_build": "feature build", "chat": "chat turn"}[kind]
	if !known {
		what = "job"
		fields = quoted(fields, "Kind", kind, false)
	}
	if jobField(job, "group") != "" {
		what = "group review"
	}
	label := what + " job"
	if id := jobField(job, "id"); validSegment(id) {
		label += " " + id
	} else {
		fields = quoted(fields, "Job", id, false)
	}
	var pr int
	_ = json.Unmarshal(job["pr"], &pr)
	switch repository := jobField(job, "repository"); {
	case repository == "":
	case !validRepository(repository):
		fields = quoted(fields, "Repository", repository, false)
	case pr > 0:
		label += " of " + repository + "#" + strconv.Itoa(pr)
	default:
		label += " in " + repository
	}
	return label, fields
}

func jobOperations() []Operation {
	return []Operation{{
		Name:        "cancel_job",
		Description: "Proposes cancelling a queued or running job, as Cancel job does: on a job's page that job, on the Jobs page a job by id (see jobs).",
		Pages:       []string{PageJobs, PageJob},
		Method:      http.MethodPost,
		Route:       "/api/jobs/{id}/cancel",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","maxLength":200,"description":"The job: on a job's page, only to check it is the page's."}},"additionalProperties":false}`),
		Reads:       []string{"/api/jobs"},
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				ID string `json:"id"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			job, err := jobOnPage(ctx, c, a.ID)
			if err != nil {
				return OperationRequest{}, err
			}
			if state := jobField(job, "state"); state != "queued" && state != "running" {
				return OperationRequest{}, errors.New("that job is not queued or running")
			}
			id := jobField(job, "id")
			return OperationRequest{Target: "job:" + id, Vars: map[string]string{"id": id}, Body: json.RawMessage(`{}`)}, nil
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			job, err := jobOnPage(ctx, c, req.Vars["id"])
			if err != nil {
				return OperationSummary{}, err
			}
			label, fields := jobLabel(job)
			return OperationSummary{Action: "Cancel the " + label, Details: fields}, nil
		},
	}}
}

// groupBody is POST /api/groups's body.
type groupBody struct {
	Name    string        `json:"name"`
	Members []groupMember `json:"members"`
}

type groupMember struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
}

func groupOperations() []Operation {
	return []Operation{{
		Name:        "create_group",
		Description: "Proposes a new review group of 2 to 8 pull requests of one repository the Pull requests page lists, as Review together does, with a name (1 to 80 characters).",
		Pages:       []string{PagePulls},
		Method:      http.MethodPost,
		Route:       "/api/groups",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","minLength":1,"maxLength":80},` +
			`"repository":{"type":"string","minLength":3,"maxLength":200},` +
			`"numbers":{"type":"array","minItems":2,"maxItems":8,"items":{"type":"integer","minimum":1}}},` +
			`"required":["name","repository","numbers"],"additionalProperties":false}`),
		Reads: []string{"/api/index"},
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				Name       string `json:"name"`
				Repository string `json:"repository"`
				Numbers    []int  `json:"numbers"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			if err := checkAgentText("name", a.Name, false); err != nil {
				return OperationRequest{}, err
			}
			body := groupBody{Name: a.Name}
			for _, n := range a.Numbers {
				e, err := pullOnPage(ctx, c, a.Repository, n)
				if err != nil {
					return OperationRequest{}, err
				}
				body.Members = append(body.Members, groupMember{e.repository(), n})
			}
			raw, err := json.Marshal(body)
			return OperationRequest{Target: TargetGroup + "new", Body: raw}, err
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			var b groupBody
			if json.Unmarshal(req.Body, &b) != nil {
				return OperationSummary{}, ErrNotCurrentPage
			}
			s := OperationSummary{Action: "Create a review group of " + strconv.Itoa(len(b.Members)) + " pull requests", Details: []SummaryField{agentWrote("Name", b.Name, false)}}
			for _, m := range b.Members {
				e, err := pullOnPage(ctx, c, m.Repository, m.Number)
				if err != nil {
					return OperationSummary{}, err
				}
				s.Details = quoted(append(s.Details, fact("Pull request", e.label())), "Title", e.title(), false)
			}
			return s, nil
		},
	}, {
		Name:         "delete_group",
		Description:  "Proposes deleting the review group the page shows, as Delete group does; its pull requests and saved reviews are kept.",
		Pages:        []string{PageGroup},
		Method:       http.MethodDelete,
		Route:        "/api/groups/{id}",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Reads:        []string{"/api/groups"},
		Irreversible: true,
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			if err := strictArgs(args, &struct{}{}); err != nil {
				return OperationRequest{}, err
			}
			if c.Page.Group == "" || !validSegment(c.Page.Group) {
				return OperationRequest{}, ErrNotCurrentPage
			}
			return OperationRequest{Target: TargetGroup + c.Page.Group, Vars: map[string]string{"id": c.Page.Group}, Body: json.RawMessage(`{}`)}, nil
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			id := req.Vars["id"]
			if id != c.Page.Group {
				return OperationSummary{}, ErrNotCurrentPage
			}
			body, missing, err := c.get(ctx, "/api/groups/"+id, nil)
			if err != nil {
				return OperationSummary{}, errors.New("the group is unavailable now")
			}
			var g struct {
				Name       string `json:"name"`
				Repository string `json:"repository"`
				Members    []int  `json:"members"`
			}
			if missing || json.Unmarshal(body, &g) != nil {
				return OperationSummary{}, errors.New("the group no longer exists")
			}
			members := make([]string, 0, len(g.Members))
			for _, n := range g.Members {
				members = append(members, "#"+strconv.Itoa(n))
			}
			repository, fields := repositoryNamed([]SummaryField{fact("Kept", "Pull requests "+strings.Join(members, ", ")+" and their saved reviews")}, g.Repository)
			return OperationSummary{Action: "Delete a review group of " + repository, Details: quoted(fields, "Group name", g.Name, false)}, nil
		},
	}}
}

// repositoryOnPage is the registered repository an operation acts on: on a
// repository's page its own (one named must be it), on the Repositories page
// one it lists.
func repositoryOnPage(ctx context.Context, c OperationContext, named string) (pickedRepository, error) {
	named = strings.TrimSpace(named)
	if c.Page.Page != PageRepositories {
		return pickedRepository{}, ErrNotCurrentPage
	}
	if c.Page.Repository != "" {
		if named != "" && !strings.EqualFold(named, c.Page.Repository) {
			return pickedRepository{}, fmt.Errorf("the person has %s's page open: an operation on this page is for that repository", c.Page.Repository)
		}
		named = c.Page.Repository
	}
	if named == "" {
		return pickedRepository{}, errors.New("name the repository as owner/name")
	}
	list, err := pickedRepositories(ctx, c)
	if err != nil {
		return pickedRepository{}, err
	}
	for _, r := range list {
		if strings.EqualFold(r.Repository, named) {
			return r, nil
		}
	}
	return pickedRepository{}, fmt.Errorf("%s is not a repository the page lists", named)
}

// repositoryVars are a repository route's {owner} and {name}.
func repositoryVars(repository string) map[string]string {
	owner, name, _ := strings.Cut(repository, "/")
	return map[string]string{"owner": owner, "name": name}
}

func repositoryOperations() []Operation {
	repositoryArg := `"repository":{"type":"string","minLength":3,"maxLength":200,"description":"The repository as owner/name: on a repository's page, only to check it is the page's."}`
	return []Operation{{
		Name:        "register_repository",
		Description: "Proposes adding a GitHub repository (owner/name) to the repositories baum watches, as picking it on the Repositories page does. It clones nothing.",
		Pages:       []string{PageRepositories},
		Method:      http.MethodPost,
		Route:       "/api/repositories",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"repository":{"type":"string","minLength":3,"maxLength":200}},"required":["repository"],"additionalProperties":false}`),
		Reads:       []string{"/api/repositories"},
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				Repository string `json:"repository"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			// The registry keeps names in lower case: that is what is sent and
			// shown, whatever the agent's spelling.
			repository := strings.ToLower(strings.TrimSpace(a.Repository))
			if !validRepository(repository) {
				return OperationRequest{}, errors.New("name the repository as owner/name")
			}
			if c.Page.Page != PageRepositories || (c.Page.Repository != "" && !strings.EqualFold(c.Page.Repository, repository)) {
				return OperationRequest{}, ErrNotCurrentPage
			}
			picked, err := pickedRepositories(ctx, c)
			if err != nil {
				return OperationRequest{}, err
			}
			if slices.ContainsFunc(picked, func(r pickedRepository) bool { return strings.EqualFold(r.Repository, repository) }) {
				return OperationRequest{}, fmt.Errorf("%s is registered already", repository)
			}
			body, err := json.Marshal(struct {
				Repository string `json:"repository"`
			}{repository})
			return OperationRequest{Target: repository, Body: body}, err
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			if c.Page.Page != PageRepositories {
				return OperationSummary{}, ErrNotCurrentPage
			}
			if c.Page.Repository != "" && !strings.EqualFold(c.Page.Repository, req.Target) {
				return OperationSummary{}, ErrNotCurrentPage
			}
			var b struct {
				Repository string `json:"repository"`
			}
			if json.Unmarshal(req.Body, &b) != nil || b.Repository != req.Target || !validRepository(b.Repository) {
				return OperationSummary{}, ErrNotCurrentPage
			}
			return OperationSummary{Action: "Add " + b.Repository + " to the repositories baum watches", Details: []SummaryField{fact("Cloned", "Nothing")}}, nil
		},
	}, {
		Name:         "remove_repository",
		Description:  "Proposes removing a repository from the repositories baum watches, as Remove repository does: its clones' registrations go too; their folders and saved reviews stay on disk. On a repository's page that repository.",
		Pages:        []string{PageRepositories},
		Method:       http.MethodDelete,
		Route:        "/api/repositories/{owner}/{name}",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{` + repositoryArg + `},"additionalProperties":false}`),
		Reads:        []string{"/api/repositories"},
		Irreversible: true,
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				Repository string `json:"repository"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			r, err := repositoryOnPage(ctx, c, a.Repository)
			if err != nil {
				return OperationRequest{}, err
			}
			return OperationRequest{Target: strings.ToLower(r.Repository), Vars: repositoryVars(r.Repository), Body: json.RawMessage(`{}`)}, nil
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			r, err := repositoryOnPage(ctx, c, req.Vars["owner"]+"/"+req.Vars["name"])
			if err != nil {
				return OperationSummary{}, err
			}
			repository, fields := repositoryNamed(nil, r.Repository)
			return OperationSummary{Action: "Stop watching " + repository,
				Details: append(fields, fact("Clones unregistered", strconv.Itoa(len(r.Clones))), fact("Kept", "Their folders and saved reviews stay on disk"))}, nil
		},
	}, {
		Name:         "remove_clone",
		Description:  "Proposes unregistering one local clone (by its id, see repositories) of a repository the page lists, as a clone's Remove does; its folder and saved reviews stay on disk.",
		Pages:        []string{PageRepositories},
		Method:       http.MethodDelete,
		Route:        "/api/repos/{id}",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"clone":{"type":"string","minLength":1,"maxLength":200}},"required":["clone"],"additionalProperties":false}`),
		Reads:        []string{"/api/repositories", "/api/repos"},
		Irreversible: true,
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				Clone string `json:"clone"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			clone, err := cloneOnPage(ctx, c, a.Clone)
			if err != nil {
				return OperationRequest{}, err
			}
			return OperationRequest{Target: "clone:" + clone.ID, Vars: map[string]string{"id": clone.ID}, Body: json.RawMessage(`{}`)}, nil
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			clone, err := cloneOnPage(ctx, c, req.Vars["id"])
			if err != nil {
				return OperationSummary{}, err
			}
			repository, fields := repositoryNamed([]SummaryField{fact("Kept", "The folder and its saved reviews stay on disk")}, clone.Repository)
			return OperationSummary{Action: "Unregister a clone of " + repository, Details: quoted(fields, "Folder", clone.Path, false)}, nil
		},
	}, {
		Name: "clone_repository",
		Description: "Proposes cloning a registered repository from GitHub, as Clone does, into the folder the app would choose (its default clone target, see clone_target): you cannot choose the folder; the person sees it before confirming, and clones elsewhere from the app. " +
			"On a repository's page that repository.",
		Pages:       []string{PageRepositories},
		Method:      http.MethodPost,
		Route:       "/api/repositories/{owner}/{name}/clone",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + repositoryArg + `},"additionalProperties":false}`),
		Reads:       []string{"/api/repositories"},
		Build: func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error) {
			var a struct {
				Repository string `json:"repository"`
			}
			if err := strictArgs(args, &a); err != nil {
				return OperationRequest{}, err
			}
			r, err := repositoryOnPage(ctx, c, a.Repository)
			if err != nil {
				return OperationRequest{}, err
			}
			vars := repositoryVars(r.Repository)
			// The folder is the app's default target, read now and sent as
			// read, so the clone goes where the person saw; the agent never
			// names one.
			if !validSegment(vars["owner"]) || !validSegment(vars["name"]) {
				return OperationRequest{}, errors.New("not a repository baum can clone")
			}
			body, missing, err := c.get(ctx, "/api/repositories/"+vars["owner"]+"/"+vars["name"]+"/clone-target", nil)
			if err != nil || missing || disabled(body) {
				return OperationRequest{}, errors.New("the clone target is unavailable now")
			}
			var target struct {
				Dir string `json:"dir"`
			}
			if json.Unmarshal(body, &target) != nil || target.Dir == "" {
				return OperationRequest{}, errors.New("the clone target is unavailable now")
			}
			// The summary shows the folder as it is sent; one it could not
			// show so is the person's to clone into from the app.
			if plain(target.Dir, false) != target.Dir {
				return OperationRequest{}, errors.New("the clone target holds characters the summary cannot show: the person clones it from the app")
			}
			raw, err := json.Marshal(target)
			return OperationRequest{Target: strings.ToLower(r.Repository), Vars: vars, Body: raw}, err
		},
		Describe: func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error) {
			r, err := repositoryOnPage(ctx, c, req.Vars["owner"]+"/"+req.Vars["name"])
			if err != nil {
				return OperationSummary{}, err
			}
			var b struct {
				Dir string `json:"dir"`
			}
			_ = json.Unmarshal(req.Body, &b)
			repository, fields := repositoryNamed([]SummaryField{fact("Then", "The clone is registered with baum when it finishes")}, r.Repository)
			return OperationSummary{Action: "Clone " + repository + " from GitHub", Details: quoted(fields, "Into folder", b.Dir, false)}, nil
		},
	}}
}

// cloneOnPage finds a registered clone, by id, of a repository the page
// lists (on a repository's page, of that repository).
func cloneOnPage(ctx context.Context, c OperationContext, id string) (registeredClone, error) {
	if c.Page.Page != PageRepositories {
		return registeredClone{}, ErrNotCurrentPage
	}
	clones, err := registeredClones(ctx, c)
	if err != nil {
		return registeredClone{}, err
	}
	for _, clone := range clones {
		if clone.ID != id {
			continue
		}
		if _, err := repositoryOnPage(ctx, c, clone.Repository); err != nil {
			return registeredClone{}, err
		}
		return clone, nil
	}
	return registeredClone{}, errors.New("that clone is not one the page lists")
}
