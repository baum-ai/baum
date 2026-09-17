package cursortranscript

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lastpersonlabs/reviewpal/sessions/sqliteread"
)

// SessionRef is the store index entry for one composer.
type SessionRef struct {
	ComposerID          string               `json:"composer_id"`
	Title               string               `json:"title,omitempty"`
	WorkspaceID         string               `json:"workspace_id,omitempty"`
	WorkspaceIdentifier *WorkspaceIdentifier `json:"-"`
	CreatedAt           int64                `json:"created_at,omitempty"`
	LastUpdatedAt       int64                `json:"last_updated_at,omitempty"`
	HasHeader           bool                 `json:"has_header"`
	HasData             bool                 `json:"has_data"`
	IsSubagent          bool                 `json:"is_subagent"`
	Header              *Header              `json:"-"`
}

// Discovery is the deduplicated union of composerHeaders and composerData keys.
type Discovery struct {
	Sessions []SessionRef `json:"sessions"`
	JunkRows int          `json:"junk_rows"`
	byID     map[string]*SessionRef
}

// Roots returns composers not classified as listed or header-flagged subagents.
func (d *Discovery) Roots() []SessionRef {
	if d == nil {
		return nil
	}
	var out []SessionRef
	for _, ref := range d.Sessions {
		if !ref.IsSubagent {
			out = append(out, ref)
		}
	}
	return out
}

func (d *Discovery) Session(composerID string) (SessionRef, bool) {
	if d == nil {
		return SessionRef{}, false
	}
	ref := d.byID[composerID]
	if ref == nil {
		return SessionRef{}, false
	}
	return *ref, true
}

// Discover indexes the DB-backed Cursor sessions in this store.
func (s *Store) Discover() (*Discovery, error) {
	if s.discovery != nil {
		return s.discovery, nil
	}
	d := &Discovery{byID: map[string]*SessionRef{}}
	ensure := func(id string) *SessionRef {
		ref := d.byID[id]
		if ref == nil {
			ref = &SessionRef{ComposerID: id}
			d.byID[id] = ref
		}
		return ref
	}

	if err := s.db.Scan(headerTable, func(row sqliteread.Row) error {
		composerID, ok := row.Text("composerId")
		if !ok || composerID == "" {
			d.JunkRows++
			return nil
		}
		valueRaw, ok := rowBytes(row, "value")
		if !ok {
			d.JunkRows++
			valueRaw = nil
		}
		value, err := decodeHeaderValue(valueRaw)
		if err != nil {
			return fmt.Errorf("composer header %s: %w", composerID, err)
		}
		header := &Header{
			ComposerID: composerID, WorkspaceID: rowText(row, "workspaceId"),
			CreatedAt: rowInt64(row, "createdAt"), LastUpdatedAt: rowInt64(row, "lastUpdatedAt"),
			IsArchived: rowInt64(row, "isArchived") != 0, IsSubagent: rowInt64(row, "isSubagent") != 0,
			Value: value,
		}
		ref := ensure(composerID)
		ref.HasHeader, ref.Header = true, header
		ref.WorkspaceID, ref.CreatedAt, ref.LastUpdatedAt = header.WorkspaceID, header.CreatedAt, header.LastUpdatedAt
		ref.IsSubagent, ref.Title = header.IsSubagent, header.Value.Name
		ref.WorkspaceIdentifier = header.Value.WorkspaceIdentifier
		return nil
	}); err != nil {
		return nil, fmt.Errorf("reading Cursor composer headers: %w", err)
	}

	listedChildren := map[string]bool{}
	if err := s.db.ScanPrefix(cursorTable, "key", composerKeyPrefix, func(row sqliteread.Row) error {
		// The range predicate guarantees a non-NULL key carrying the prefix.
		key, _ := row.Text("key")
		id := strings.TrimPrefix(key, composerKeyPrefix)
		raw, ok := rowBytes(row, "value")
		if !ok || isNull(raw) {
			d.JunkRows++
			return nil
		}
		data, err := DecodeComposerData(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if data.ComposerID != "" && data.ComposerID != id {
			return fmt.Errorf("%s contains composerId %s", key, data.ComposerID)
		}
		if data.ComposerID == "" {
			data.ComposerID = id
		}
		ref := ensure(id)
		ref.HasData = true
		if ref.WorkspaceIdentifier == nil {
			ref.WorkspaceIdentifier = data.WorkspaceIdentifier
		}
		if ref.Title == "" {
			ref.Title = data.Name
		}
		if ref.CreatedAt == 0 {
			ref.CreatedAt = data.CreatedAt
		}
		if ref.LastUpdatedAt == 0 && data.LastUpdatedAt != nil {
			ref.LastUpdatedAt = *data.LastUpdatedAt
		}
		for _, child := range data.SubagentComposerIDs {
			if child != "" {
				listedChildren[child] = true
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("reading Cursor composer data: %w", err)
	}

	for child := range listedChildren {
		if ref := d.byID[child]; ref != nil {
			ref.IsSubagent = true
		}
	}
	for _, ref := range d.byID {
		d.Sessions = append(d.Sessions, *ref)
	}
	sort.Slice(d.Sessions, func(i, j int) bool {
		if d.Sessions[i].CreatedAt != d.Sessions[j].CreatedAt {
			return d.Sessions[i].CreatedAt < d.Sessions[j].CreatedAt
		}
		return d.Sessions[i].ComposerID < d.Sessions[j].ComposerID
	})
	// Repoint the lookup at the stable slice rather than the temporary map rows.
	d.byID = make(map[string]*SessionRef, len(d.Sessions))
	for i := range d.Sessions {
		d.byID[d.Sessions[i].ComposerID] = &d.Sessions[i]
	}
	s.discovery = d
	return d, nil
}

// Composer is one materialized composer and its ordered/unordered bubbles.
type Composer struct {
	Header   *Header          `json:"header,omitempty"`
	Data     *ComposerData    `json:"data"`
	Bubbles  []*Bubble        `json:"bubbles"`
	Unlisted []*Bubble        `json:"unlisted_bubbles,omitempty"`
	Diffs    []*CodeBlockDiff `json:"-"`
}

// Tree is one composer and every materialized child listed beneath it.
type Tree struct {
	Composer         *Composer `json:"composer"`
	Children         []*Tree   `json:"children,omitempty"`
	DanglingChildren []string  `json:"dangling_children,omitempty"`
}

func (t *Tree) ComposerID() string {
	if t == nil || t.Composer == nil || t.Composer.Data == nil {
		return ""
	}
	return t.Composer.Data.ComposerID
}

// Composers returns the tree in parent-first order.
func (t *Tree) Composers() []*Composer {
	if t == nil || t.Composer == nil {
		return nil
	}
	out := []*Composer{t.Composer}
	for _, child := range t.Children {
		out = append(out, child.Composers()...)
	}
	return out
}

// Tree reads one composer tree in authoritative header order.
func (s *Store) Tree(composerID string) (*Tree, error) {
	discovery, err := s.Discover()
	if err != nil {
		return nil, err
	}
	ref, ok := discovery.Session(composerID)
	if !ok || !ref.HasData {
		return nil, fmt.Errorf("composer %s has no composerData record", composerID)
	}
	claimed := map[string]bool{}
	return s.readTree(composerID, claimed, map[string]bool{})
}

func (s *Store) readTree(composerID string, claimed, ancestry map[string]bool) (*Tree, error) {
	if ancestry[composerID] {
		return nil, fmt.Errorf("composer %s is part of a subagent cycle", composerID)
	}
	if claimed[composerID] {
		return nil, fmt.Errorf("composer %s is listed as a child more than once", composerID)
	}
	claimed[composerID] = true
	ancestry = cloneSet(ancestry)
	ancestry[composerID] = true

	composer, err := s.readComposer(composerID)
	if err != nil {
		return nil, err
	}
	tree := &Tree{Composer: composer}
	discovery, _ := s.Discover()
	seenChild := map[string]bool{}
	for _, childID := range composer.Data.SubagentComposerIDs {
		if childID == "" || seenChild[childID] {
			continue
		}
		seenChild[childID] = true
		ref, ok := discovery.Session(childID)
		if !ok || !ref.HasData {
			tree.DanglingChildren = append(tree.DanglingChildren, childID)
			continue
		}
		child, err := s.readTree(childID, claimed, ancestry)
		if err != nil {
			return nil, err
		}
		if len(child.Composer.Bubbles) == 0 && len(child.Composer.Unlisted) == 0 {
			tree.DanglingChildren = append(tree.DanglingChildren, childID)
			continue
		}
		tree.Children = append(tree.Children, child)
	}
	slices.Sort(tree.DanglingChildren)
	return tree, nil
}

func (s *Store) readComposer(composerID string) (*Composer, error) {
	key := composerKeyPrefix + composerID
	var data *ComposerData
	if err := s.db.ScanPrefix(cursorTable, "key", key, func(row sqliteread.Row) error {
		rowKey, ok := row.Text("key")
		if !ok || rowKey != key {
			return nil
		}
		if data != nil {
			return fmt.Errorf("duplicate key %s", key)
		}
		raw, ok := rowBytes(row, "value")
		if !ok || isNull(raw) {
			return nil
		}
		decoded, err := DecodeComposerData(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		data = decoded
		return nil
	}); err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("composer %s has no readable composerData record", composerID)
	}
	if data.ComposerID == "" {
		data.ComposerID = composerID
	}
	if data.ComposerID != composerID {
		return nil, fmt.Errorf("%s contains composerId %s", key, data.ComposerID)
	}

	byID := map[string]*Bubble{}
	var all []*Bubble
	prefix := bubbleKeyPrefix + composerID + ":"
	if err := s.db.ScanPrefix(cursorTable, "key", prefix, func(row sqliteread.Row) error {
		rowKey, ok := row.Text("key")
		if !ok {
			return nil
		}
		raw, ok := rowBytes(row, "value")
		if !ok || isNull(raw) {
			return nil
		}
		bubble, err := DecodeBubble(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", rowKey, err)
		}
		if bubble == nil {
			return nil
		}
		id := strings.TrimPrefix(rowKey, prefix)
		if bubble.BubbleID == "" {
			bubble.BubbleID = id
		}
		if bubble.BubbleID != id {
			return fmt.Errorf("%s contains bubbleId %s", rowKey, bubble.BubbleID)
		}
		if byID[id] != nil {
			return fmt.Errorf("composer %s has duplicate bubble %s", composerID, id)
		}
		bubble.Key, bubble.ComposerID = rowKey, composerID
		byID[id] = bubble
		all = append(all, bubble)
		return nil
	}); err != nil {
		return nil, err
	}

	composer := &Composer{Data: data, Diffs: make([]*CodeBlockDiff, 0)}
	discovery, _ := s.Discover()
	if discovery != nil {
		if ref, ok := discovery.Session(composerID); ok {
			composer.Header = ref.Header
		}
	}
	diffPrefix := diffKeyPrefix + composerID + ":"
	var diffJunk int
	if err := s.db.ScanPrefix(cursorTable, "key", diffPrefix, func(row sqliteread.Row) error {
		rowKey, ok := row.Text("key")
		if !ok {
			return fmt.Errorf("composer %s has a code block diff without a text key", composerID)
		}
		diffID := strings.TrimPrefix(rowKey, diffPrefix)
		if diffID == "" || strings.Contains(diffID, ":") {
			return fmt.Errorf("composer %s has malformed code block diff identity %s", composerID, rowKey)
		}
		raw, ok := rowBytes(row, "value")
		if !ok || isNull(raw) {
			diffJunk++
			return nil
		}
		diff, err := DecodeCodeBlockDiff(raw)
		if err != nil || diff == nil {
			diffJunk++
			return nil
		}
		diff.DiffID = diffID
		composer.Diffs = append(composer.Diffs, diff)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(composer.Diffs, func(i, j int) bool { return composer.Diffs[i].DiffID < composer.Diffs[j].DiffID })
	if discovery != nil {
		if s.diffJunkByComposerID == nil {
			s.diffJunkByComposerID = map[string]int{}
		}
		discovery.JunkRows += diffJunk - s.diffJunkByComposerID[composerID]
		s.diffJunkByComposerID[composerID] = diffJunk
	}
	listed := map[string]bool{}
	for _, ref := range data.Headers {
		bubble := byID[ref.BubbleID]
		if bubble == nil {
			return nil, fmt.Errorf("composer %s lists missing bubble %s", composerID, ref.BubbleID)
		}
		if bubble.Type != ref.Type {
			return nil, fmt.Errorf("composer %s lists bubble %s as type %d, record has %d", composerID, ref.BubbleID, ref.Type, bubble.Type)
		}
		if listed[ref.BubbleID] {
			return nil, fmt.Errorf("composer %s lists bubble %s more than once", composerID, ref.BubbleID)
		}
		listed[ref.BubbleID] = true
		composer.Bubbles = append(composer.Bubbles, bubble)
	}
	for _, bubble := range all {
		if !listed[bubble.BubbleID] {
			composer.Unlisted = append(composer.Unlisted, bubble)
		}
	}
	sort.Slice(composer.Unlisted, func(i, j int) bool { return composer.Unlisted[i].Key < composer.Unlisted[j].Key })
	return composer, nil
}

func cloneSet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func rowBytes(row sqliteread.Row, column string) ([]byte, bool) {
	value, ok := row.Value(column)
	if !ok || value == nil {
		return nil, false
	}
	switch value := value.(type) {
	case string:
		return []byte(value), true
	case []byte:
		return value, true
	default:
		return nil, false
	}
}

func rowText(row sqliteread.Row, column string) string {
	value, _ := row.Text(column)
	return value
}

func rowInt64(row sqliteread.Row, column string) int64 {
	value, _ := row.Value(column)
	switch value := value.(type) {
	case int64:
		return value
	case float64:
		return int64(value)
	}
	return 0
}
