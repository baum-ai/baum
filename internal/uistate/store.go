// Package uistate keeps the person's inbox bookkeeping and review drafts in
// the application database, independently of a browser's loopback origin.
package uistate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
)

const KeepWalkthroughs = 40

var ErrInvalid = errors.New("invalid saved UI state")
var ErrUnreadable = errors.New("saved UI state is unreadable")
var clientKey = regexp.MustCompile(`^[A-Za-z0-9_-]{12,64}$`)
var mutationKey = regexp.MustCompile(`^([A-Za-z0-9_-]{12,64}):([1-9][0-9]{0,15})$`)
var dayKey = regexp.MustCompile(`^[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}$`)
var reviewKey = regexp.MustCompile(`^[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+){1,2}#[1-9][0-9]*@v[1-9][0-9]*$`)

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
	return kind == "inbox" && key == "" || kind == "walkthrough" && len(key) <= 300 && reviewKey.MatchString(key)
}
func empty() Record { return Record{Value: json.RawMessage("null")} }
func decode(raw []byte, kind, key string) (Record, error) {
	if len(raw) == 0 {
		return empty(), nil
	}
	var record Record
	if kind == "walkthrough" {
		var records map[string]*struct {
			Record
			At *int64 `json:"at"`
		}
		if json.Unmarshal(raw, &records) != nil || records == nil {
			return Record{}, ErrUnreadable
		}
		for review, item := range records {
			if !valid("walkthrough", review) || item == nil || item.At == nil || validRecord("walkthrough", item.Record) != nil {
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
		if kind == "inbox" {
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
		for _, k := range keys[min(KeepWalkthroughs, len(keys)):] {
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
		}
		if json.Unmarshal(raw, &value) != nil || value.Day == nil || !dayKey.MatchString(*value.Day) || value.Seen == nil || value.Snoozed == nil || value.Held == nil || len(*value.Seen) > 2000 || len(*value.Snoozed) > 2000 || len(*value.Held) > 2000 {
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
	default:
		return ErrInvalid
	}
	return nil
}
