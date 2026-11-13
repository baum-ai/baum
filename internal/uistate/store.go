// Package uistate keeps the person's inbox bookkeeping and review drafts in
// the application database, independently of a browser's loopback origin.
package uistate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/inboxes"
)

const KeepWalkthroughs = 40

// KeepReviews is how many pull requests' pending reviews are retained.
const KeepReviews = 200

// MaxReviewComments is how many comments one pending review holds.
const MaxReviewComments = 100

// KeepSent is how many sent reviews a pull request's record keeps.
const KeepSent = 20

// MaxTabs is how many saved Pull requests tabs the filters document holds.
const MaxTabs = 20

var ErrInvalid = errors.New("invalid saved UI state")
var ErrUnreadable = errors.New("saved UI state is unreadable")
var clientKey = regexp.MustCompile(`^[A-Za-z0-9_-]{12,64}$`)
var mutationKey = regexp.MustCompile(`^([A-Za-z0-9_-]{12,64}):([1-9][0-9]{0,15})$`)
var dayKey = regexp.MustCompile(`^[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}$`)
var filterPage = regexp.MustCompile(`^(inbox|pulls|(inbox|tab):[0-9a-f]{32})$`)
var tabID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var tabView = regexp.MustCompile(`^(open|history)$`)
var reviewKey = regexp.MustCompile(`^[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+){1,2}#[1-9][0-9]*@v[1-9][0-9]*$`)
var pullKey = regexp.MustCompile(`^[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+){1,2}#[1-9][0-9]*$`)
var commentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var headSHA = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

type Record struct {
	Revision int               `json:"revision"`
	Value    json.RawMessage   `json:"value"`
	Clients  map[string]uint64 `json:"clients"`
}
type retained struct {
	Record
	At int64 `json:"at"`
}
type StaleError struct{ Current Record }

func (e *StaleError) Error() string { return "Saved state changed in another window." }

type Store struct {
	Path string
	Now  func() time.Time
}

func valid(kind, key string) bool {
	switch kind {
	case "inbox", "tour", "filters":
		return key == ""
	case "walkthrough":
		return len(key) <= 300 && reviewKey.MatchString(key)
	case "review":
		return len(key) <= 300 && pullKey.MatchString(key)
	}
	return false
}

// keep is how many records a keyed kind retains, the latest saved first; 0
// for a kind that is one record.
func keep(kind string) int {
	switch kind {
	case "walkthrough":
		return KeepWalkthroughs
	case "review":
		return KeepReviews
	}
	return 0
}
func empty() Record { return Record{Value: json.RawMessage("null")} }
func decode(raw []byte, kind, key string) (Record, error) {
	if len(raw) == 0 {
		return empty(), nil
	}
	var record Record
	if keep(kind) > 0 {
		var records map[string]*struct {
			Record
			At *int64 `json:"at"`
		}
		if json.Unmarshal(raw, &records) != nil || records == nil {
			return Record{}, ErrUnreadable
		}
		for review, item := range records {
			if !valid(kind, review) || item == nil || item.At == nil || validRecord(kind, item.Record) != nil {
				return Record{}, ErrUnreadable
			}
		}
		item, found := records[key]
		if !found {
			return empty(), nil
		}
		record = item.Record
	} else if json.Unmarshal(raw, &record) != nil {
		return Record{}, ErrUnreadable
	}
	if validRecord(kind, record) != nil {
		// Filters are only how pages were last narrowed: a document in an
		// older shape reads as none, and the next save replaces it.
		if kind == "filters" {
			record.Value = json.RawMessage("null")
			return record, nil
		}
		return Record{}, ErrUnreadable
	}
	return record, nil
}
func (s *Store) Read(kind, key string) (Record, error) {
	if !valid(kind, key) {
		return Record{}, ErrInvalid
	}
	raw, err := appdb.Read(s.Path, "ui", kind)
	if errors.Is(err, fs.ErrNotExist) {
		return empty(), nil
	}
	if err != nil {
		return Record{}, err
	}
	return decode(raw, kind, key)
}
func validRecord(kind string, record Record) error {
	if record.Revision < 1 || record.Clients == nil || validate(kind, record.Value) != nil {
		return ErrUnreadable
	}
	for client, sequence := range record.Clients {
		if !clientKey.MatchString(client) || sequence == 0 {
			return ErrUnreadable
		}
	}
	return nil
}
func (s *Store) Replace(kind, key string, revision int, mutation string, value json.RawMessage) (Record, error) {
	match := mutationKey.FindStringSubmatch(mutation)
	if !valid(kind, key) || revision < 0 || len(match) != 3 || validate(kind, value) != nil {
		return Record{}, ErrInvalid
	}
	sequence, err := strconv.ParseUint(match[2], 10, 64)
	if err != nil {
		return Record{}, ErrInvalid
	}
	client := match[1]
	var next Record
	err = appdb.Update(s.Path, "ui", kind, func(raw []byte) ([]byte, error) {
		current, err := decode(raw, kind, key)
		if err != nil {
			return nil, err
		}
		// One client submits batches in order. Its high-water mark acknowledges
		// every committed batch without evicting retries after later keystrokes.
		if sequence <= current.Clients[client] {
			next = current
			return raw, nil
		}
		if current.Revision != revision {
			return nil, &StaleError{current}
		}
		clients := maps.Clone(current.Clients)
		if clients == nil {
			clients = map[string]uint64{}
		}
		clients[client] = sequence
		next = Record{Revision: revision + 1, Value: value, Clients: clients}
		if keep(kind) == 0 {
			return json.Marshal(next)
		}
		records := map[string]retained{}
		if len(raw) != 0 && (json.Unmarshal(raw, &records) != nil || records == nil) {
			return nil, ErrUnreadable
		}
		now := time.Now()
		if s.Now != nil {
			now = s.Now()
		}
		at := now.UnixNano()
		// Write order, not a wall clock jump, decides which drafts are retained.
		for _, record := range records {
			if record.At >= at {
				at = record.At + 1
			}
		}
		records[key] = retained{next, at}
		keys := make([]string, 0, len(records))
		for k := range records {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := records[keys[i]], records[keys[j]]
			if a.At == b.At {
				return keys[i] < keys[j]
			}
			return a.At > b.At
		})
		for _, k := range keys[min(keep(kind), len(keys)):] {
			delete(records, k)
		}
		return json.Marshal(records)
	})
	return next, err
}

// Values are bounded typed records, not an arbitrary filesystem or settings API.
func validate(kind string, raw []byte) error {
	if len(raw) == 0 || len(raw) > 384<<10 {
		return ErrInvalid
	}
	switch kind {
	case "inbox":
		var value struct {
			Day  *string `json:"day"`
			Seen *map[string]*struct {
				Kind       *string  `json:"kind"`
				Title      *string  `json:"title"`
				Repository *string  `json:"repository"`
				Number     *int     `json:"number"`
				Minutes    *float64 `json:"minutes"`
				Cleared    *int64   `json:"cleared"`
			} `json:"seen"`
			Snoozed *map[string]*int64  `json:"snoozed"`
			Held    *map[string]*string `json:"held"`
			// Archived is absent from records saved before archiving existed.
			Archived map[string]*int64 `json:"archived"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Day == nil || !dayKey.MatchString(*value.Day) || value.Seen == nil || value.Snoozed == nil || value.Held == nil || len(*value.Seen) > 2000 || len(*value.Snoozed) > 2000 || len(*value.Held) > 2000 || len(value.Archived) > 2000 {
			return ErrInvalid
		}
		if _, err := time.Parse("2006-1-2", *value.Day); err != nil {
			return ErrInvalid
		}
		for key, item := range *value.Seen {
			if len(key) > 300 || item == nil || item.Number == nil || *item.Number < 1 || item.Kind == nil || item.Repository == nil || *item.Repository == "" || len(*item.Repository) > 255 || item.Title == nil || len(*item.Title) > 4096 {
				return ErrInvalid
			}
			switch *item.Kind {
			case "review", "merge", "fix", "update", "clone":
			default:
				return ErrInvalid
			}
		}
		for key, until := range *value.Snoozed {
			if len(key) > 300 || until == nil || *until < 0 {
				return ErrInvalid
			}
		}
		for key, head := range *value.Held {
			if len(key) > 300 || head == nil || len(*head) > 128 {
				return ErrInvalid
			}
		}
		for key, at := range value.Archived {
			if len(key) > 300 || at == nil || *at < 0 {
				return ErrInvalid
			}
		}
	case "walkthrough":
		var value struct {
			Seen  *[]string           `json:"seen"`
			Notes *map[string]*string `json:"notes"`
			Note  *string             `json:"note"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Seen == nil || value.Notes == nil || value.Note == nil || len(*value.Seen) > 2000 || len(*value.Notes) > 2000 || len(*value.Note) > 64<<10 {
			return ErrInvalid
		}
		for _, id := range *value.Seen {
			if id == "" || len(id) > 256 {
				return ErrInvalid
			}
		}
		for id, note := range *value.Notes {
			if id == "" || len(id) > 256 || note == nil || len(*note) > 64<<10 {
				return ErrInvalid
			}
		}
	case "review":
		var value struct {
			Comments *[]*reviewComment `json:"comments"`
			Note     *string           `json:"note"`
			// Absent from a record saved before sent reviews were kept.
			Sent []*struct {
				URL      *string `json:"url"`
				Event    *string `json:"event"`
				At       *int64  `json:"at"`
				Note     *string `json:"note"`
				Comments *[]*struct {
					Title *string `json:"title"`
					Where *string `json:"where"`
					Body  *string `json:"body"`
				} `json:"comments"`
			} `json:"sent"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Comments == nil || value.Note == nil || len(*value.Comments) > MaxReviewComments || len(*value.Note) > 64<<10 || len(value.Sent) > KeepSent {
			return ErrInvalid
		}
		for _, sent := range value.Sent {
			if sent == nil || sent.URL == nil || !strings.HasPrefix(*sent.URL, "https://") || len(*sent.URL) > 2048 || sent.Event == nil || sent.At == nil || *sent.At < 0 || sent.Note == nil || len(*sent.Note) > 64<<10 || sent.Comments == nil || len(*sent.Comments) > MaxReviewComments {
				return ErrInvalid
			}
			switch *sent.Event {
			case "APPROVE", "COMMENT", "REQUEST_CHANGES":
			default:
				return ErrInvalid
			}
			for _, comment := range *sent.Comments {
				if comment == nil || comment.Title == nil || len(*comment.Title) > 1024 || comment.Where == nil || len(*comment.Where) > 512 || comment.Body == nil || len(*comment.Body) > 64<<10 {
					return ErrInvalid
				}
			}
		}
		ids := map[string]bool{}
		for _, comment := range *value.Comments {
			if comment == nil || checkReviewComment(*comment) != nil || ids[*comment.ID] {
				return ErrInvalid
			}
			ids[*comment.ID] = true
		}
	case "filters":
		var value struct {
			Pages *map[string]*string `json:"pages"`
			Tabs  []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				View  string `json:"view"`
				Query string `json:"query"`
			} `json:"tabs"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Pages == nil || len(*value.Pages) > 64 || len(value.Tabs) > MaxTabs {
			return ErrInvalid
		}
		for page, query := range *value.Pages {
			if !filterPage.MatchString(page) || query == nil || !inboxes.ValidQuery(*query) {
				return ErrInvalid
			}
		}
		ids := map[string]bool{}
		for _, tab := range value.Tabs {
			name := strings.TrimSpace(tab.Name)
			if !tabID.MatchString(tab.ID) || ids[tab.ID] || name == "" || name != tab.Name || utf8.RuneCountInString(name) > 40 || !tabView.MatchString(tab.View) || !inboxes.ValidQuery(tab.Query) {
				return ErrInvalid
			}
			ids[tab.ID] = true
		}
	case "tour":
		var value struct {
			Seen *bool `json:"seen"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Seen == nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// reviewComment is one comment of a pending review as the review record
// holds it.
type reviewComment struct {
	ID     *string `json:"id"`
	Target *struct {
		Kind  *string `json:"kind"`
		ID    *string `json:"id"`
		Title *string `json:"title"`
	} `json:"target"`
	Body  *string `json:"body"`
	Place *struct {
		Path      *string `json:"path"`
		Line      *int    `json:"line"`
		Side      *string `json:"side"`
		StartLine *int    `json:"start_line"`
	} `json:"place"`
	Head    *string `json:"head"`
	Created *int64  `json:"created"`
}

// MaxCommentBody is the longest body, in bytes, of a pending review comment.
const MaxCommentBody = 64 << 10

// checkReviewComment refuses a comment a review record does not hold, saying
// why; each reason wraps ErrInvalid.
func checkReviewComment(c reviewComment) error {
	refuse := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalid, reason) }
	if c.ID == nil || !commentID.MatchString(*c.ID) {
		return refuse("a comment's id is 1 to 64 letters, digits, - or _")
	}
	if c.Body == nil || strings.TrimSpace(*c.Body) == "" || len(*c.Body) > MaxCommentBody {
		return refuse(fmt.Sprintf("a comment's body is text that is not blank, of at most %d bytes", MaxCommentBody))
	}
	if c.Head == nil || (*c.Head != "" && !headSHA.MatchString(*c.Head)) {
		return refuse("a comment's head is empty or a commit's hex id")
	}
	if c.Created == nil || *c.Created < 0 {
		return refuse("a comment's creation time is not negative")
	}
	target := c.Target
	if target == nil || target.Kind == nil || target.ID == nil || target.Title == nil || len(*target.ID) > 4096 || len(*target.Title) > 1024 {
		return refuse("a comment's target has a kind, an id of at most 4096 bytes and a title of at most 1024")
	}
	switch *target.Kind {
	case "general":
	case "section", "risk", "file":
		if *target.ID == "" {
			return refuse("a section, risk or file target names its id")
		}
	default:
		return refuse("a comment's target is general, section, risk or file")
	}
	if place := c.Place; place != nil {
		if place.Path == nil || *place.Path == "" || len(*place.Path) > 4096 || place.Line == nil || *place.Line < 1 || place.Side == nil || (*place.Side != "RIGHT" && *place.Side != "LEFT") || (place.StartLine != nil && (*place.StartLine < 1 || *place.StartLine >= *place.Line)) {
			return refuse("a comment's place is a file's path, a line from 1, the side RIGHT or LEFT, and a start line before the line")
		}
	}
	return nil
}

// CheckReviewComment refuses a pending review comment ({id, target, body,
// place, head, created}) that a review record would not hold, saying why;
// the error wraps ErrInvalid. Whether its id is unused in the review is the
// caller's to check.
func CheckReviewComment(comment json.RawMessage) error {
	var c reviewComment
	if json.Unmarshal(comment, &c) != nil {
		return fmt.Errorf("%w: a comment is a JSON object", ErrInvalid)
	}
	return checkReviewComment(c)
}
