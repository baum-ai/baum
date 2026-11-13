// Package sessionimages reads retained images from native coding transcripts.
// It never fetches URLs or opens image paths mentioned in transcript content.
package sessionimages

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/vendors"
)

const (
	HumanInput = "human_input"
	ToolResult = "tool_result"
)

// Source identifies graph entities, never vendor IDs or local file paths.
type Source struct {
	Kind           string
	SessionID      string
	ChildSessionID string
	TurnID         string
	ToolUseID      string
	ToolName       string
	Position       int
	Timestamp      *time.Time
}

type Image struct {
	Bytes     []byte
	MediaType string
	Source    Source
}

type Diagnostic struct {
	SessionID string
	Reason    string
}

type Result struct {
	Images      []Image
	Diagnostics []Diagnostic
}

// Limits bound retained bytes and transcript scanning independently.
// Nonpositive values select defaults.
type Limits struct {
	MaxImages     int
	MaxImageBytes int64
	MaxTotalBytes int64
	MaxLineBytes  int
	MaxScanBytes  int64
}

func (l Limits) defaults() Limits {
	if l.MaxImages <= 0 {
		l.MaxImages = 20
	}
	if l.MaxImageBytes <= 0 {
		l.MaxImageBytes = 10 << 20
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = 64 << 20
	}
	if l.MaxLineBytes <= 0 {
		l.MaxLineBytes = 16 << 20
	}
	if l.MaxScanBytes <= 0 {
		l.MaxScanBytes = 256 << 20
	}
	return l
}

type entity struct {
	source Source
	human  bool
}
type fileIndex struct {
	inputs     map[string]entity
	inputLines map[int]entity
	tools      map[string]entity
}
type reader struct {
	ctx       context.Context
	limits    Limits
	result    Result
	total     int64
	scanned   int64
	root      string
	id        string
	storeBase string
	positions map[string]int
	files     map[string]*fileIndex
	// humanKinds records which Codex record kind supplied a human input's
	// images, so a prompt retained in both forms is not counted twice.
	humanKinds map[string]string
}

// Read extracts payloads attached to reachable graph inputs or tool calls.
// Entire, unsupported sources and review-generation sessions supply no images.
// Diagnostics contain no transcript text, local paths or encoded payloads.
func Read(ctx context.Context, s sessions.Session, limits Limits) Result {
	if s.Source != vendors.ClaudeCode && s.Source != vendors.Codex {
		return Result{Diagnostics: []Diagnostic{{SessionID: s.ID, Reason: "native image source unsupported"}}}
	}
	if s.Graph == nil || s.Graph.Root() == nil || sessions.IsGenerationRun(s.Graph) {
		return Result{}
	}
	r := &reader{ctx: ctx, limits: limits.defaults(), root: s.Graph.Root().ID, id: s.ID, files: map[string]*fileIndex{}, positions: map[string]int{}, humanKinds: map[string]string{}}
	r.index(s.Graph)
	names := make([]string, 0, len(r.files))
	main := codextranscript.PlainName(filepath.Base(s.Path))
	if _, ok := r.files[main]; ok {
		names = append(names, main)
	}
	var children []string
	for name := range r.files {
		if name != main {
			children = append(children, name)
		}
	}
	sort.Strings(children)
	names = append(names, children...)
	paths := r.paths(s, names)
	for _, name := range names {
		if r.ctx.Err() != nil || len(r.result.Images) >= r.limits.MaxImages || r.total >= r.limits.MaxTotalBytes {
			r.note("session image budget reached")
			break
		}
		path, ok := paths[name]
		if !ok {
			r.note("native subagent transcript unavailable")
			continue
		}
		r.readFile(path, r.files[name], s.Source)
	}
	if len(r.result.Images) >= r.limits.MaxImages {
		r.note("session image count limit reached")
	}
	return r.result
}

func (r *reader) note(reason string) {
	if len(r.result.Diagnostics) < 64 {
		r.result.Diagnostics = append(r.result.Diagnostics, Diagnostic{SessionID: r.id, Reason: reason})
	}
}

func (r *reader) index(g *session.Graph) {
	reachable := map[string]bool{r.root: true}
	uses := map[string]session.ToolUse{}
	for _, u := range g.ToolUses {
		uses[u.ID] = u
	}
	for changed := true; changed; {
		changed = false
		for _, s := range g.Sessions {
			if reachable[s.ID] || s.ParentSessionID == nil || s.SpawnedByToolUseID == nil || !reachable[*s.ParentSessionID] {
				continue
			}
			use, ok := uses[*s.SpawnedByToolUseID]
			if ok && use.SessionID == *s.ParentSessionID {
				reachable[s.ID] = true
				changed = true
			}
		}
	}
	inputs := map[string]session.UserInput{}
	for _, input := range g.UserInputs {
		if reachable[input.SessionID] {
			inputs[input.ID] = input
		}
	}
	for _, v := range g.VendorIDs {
		if !reachable[v.SessionID] || v.SourceRef == nil {
			continue
		}
		name, ordinal := sourceLocation(*v.SourceRef)
		if name == "" {
			continue
		}
		f := r.files[name]
		if f == nil {
			f = &fileIndex{inputs: map[string]entity{}, inputLines: map[int]entity{}, tools: map[string]entity{}}
			r.files[name] = f
		}
		switch v.OurType {
		case session.EntityUserInput:
			input, ok := inputs[v.OurID]
			if !ok {
				continue
			}
			source := r.source(input.SessionID, input.ID, input.TriggeredAt)
			source.Kind = HumanInput
			ent := entity{source: source, human: input.Origin == session.OriginHuman}
			if v.VendorIDKind == "uuid" {
				f.inputs[v.VendorID] = ent
			}
			if ordinal > 0 {
				f.inputLines[ordinal] = ent
			}
		case session.EntityToolUse:
			use, ok := uses[v.OurID]
			if !ok {
				continue
			}
			if _, ok := inputs[use.UserInputID]; !ok {
				continue
			}
			source := r.source(use.SessionID, use.UserInputID, use.Timestamp)
			source.Kind, source.ToolUseID, source.ToolName = ToolResult, use.ID, use.Name
			if v.VendorIDKind == "tool_use.id" || v.VendorIDKind == "call_id" || v.VendorIDKind == "item.id" {
				f.tools[v.VendorID] = entity{source: source}
			}
		}
	}
}

func (r *reader) source(owner, input string, at *time.Time) Source {
	source := Source{SessionID: r.id, TurnID: input, Timestamp: at}
	if owner != r.root {
		source.ChildSessionID = owner
	}
	return source
}

func sourceLocation(ref string) (string, int) {
	name, line, ok := strings.Cut(ref, "#")
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".jsonl") {
		return "", 0
	}
	if !ok {
		return name, 0
	}
	ordinal, _ := strconv.Atoi(line)
	return name, ordinal
}

// Child names come from graph source refs. Only native transcript directories
// are searched; image paths recorded in transcript content are never followed.
func (r *reader) paths(s sessions.Session, names []string) map[string]string {
	r.storeBase = filepath.Dir(s.Path)
	ownName := codextranscript.PlainName(filepath.Base(s.Path))
	found := map[string]string{ownName: s.Path}
	if len(names) == 0 || (len(names) == 1 && names[0] == ownName) {
		return found
	}
	if s.Source == vendors.ClaudeCode {
		stem := strings.TrimSuffix(filepath.Base(s.Path), ".jsonl")
		base := filepath.Join(filepath.Dir(s.Path), stem, "subagents")
		for _, name := range names {
			if name != filepath.Base(s.Path) && strings.HasPrefix(name, "agent-") {
				found[name] = filepath.Join(base, name)
			}
		}
		return found
	}
	base := filepath.Dir(s.Path)
	for d := base; d != filepath.Dir(d); d = filepath.Dir(d) {
		if filepath.Base(d) == "sessions" {
			base = d
			break
		}
	}
	r.storeBase = base
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	entries := 0
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		entries++
		if entries > 100000 {
			return fs.SkipAll
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		name := codextranscript.PlainName(d.Name())
		if !d.IsDir() && wanted[name] {
			if name != d.Name() && s.Source == vendors.Codex {
				// A compressed rollout is read only when Codex kept no plain one.
				if _, err := os.Stat(codextranscript.PlainPath(path)); err == nil {
					return nil
				}
			}
			if prior, ok := found[name]; ok && prior != path {
				found[name] = ""
			} else {
				found[name] = path
			}
		}
		return nil
	})
	if entries > 100000 {
		r.note("native transcript discovery limit reached")
	}
	return found
}

func (r *reader) readFile(path string, index *fileIndex, vendor vendors.Slug) {
	if path == "" {
		r.note("native transcript source ambiguous")
		return
	}
	rel, err := filepath.Rel(r.storeBase, path)
	if err != nil {
		r.note("native transcript unavailable")
		return
	}
	root, err := os.OpenRoot(r.storeBase)
	if err != nil {
		r.note("native transcript unavailable")
		return
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(rel)
	if err != nil {
		r.note("native transcript unavailable")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		r.note("native transcript unavailable")
		return
	}
	var src io.Reader = f
	if codextranscript.Compressed(path) {
		rc, err := codextranscript.Decompress(f)
		if err != nil {
			r.note("native transcript unavailable")
			return
		}
		defer func() { _ = rc.Close() }()
		src = rc
	}
	scan := bufio.NewReaderSize(src, 64<<10)
	turns := map[string]entity{}
	var activeTurn codexTurn
	var waiting []pendingCodex
	for line := 1; ; line++ {
		if r.ctx.Err() != nil || len(r.result.Images) >= r.limits.MaxImages || r.total >= r.limits.MaxTotalBytes {
			return
		}
		raw, oversized, err := r.line(scan)
		if oversized {
			r.note("native image record exceeds line limit")
		}
		if len(raw) > 0 && !oversized {
			if vendor == vendors.ClaudeCode {
				r.claude(raw, index)
			} else {
				p, turn, ent := r.codex(raw, index, line, &activeTurn)
				if turn != "" {
					turns[turn] = ent
					kept := waiting[:0]
					for _, pending := range waiting {
						if pending.turn == turn {
							r.collectHuman(pending.blocks, ent, pending.at, codexResponseKind)
						} else {
							kept = append(kept, pending)
						}
					}
					waiting = kept
				}
				if p != nil {
					if ent, ok := turns[p.turn]; ok {
						r.collectHuman(p.blocks, ent, p.at, codexResponseKind)
					} else if len(waiting) < r.limits.MaxImages {
						waiting = append(waiting, *p)
					}
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.note("native transcript scan limit reached or read failed")
			}
			break
		}
	}
	for _, p := range waiting {
		if ent, ok := turns[p.turn]; ok {
			r.collectHuman(p.blocks, ent, p.at, codexResponseKind)
		}
	}
}

const (
	codexResponseKind = "response_item"
	codexItemKind     = "item_completed"
)

// collectHuman collects a confirmed human input's images from one Codex record
// kind. Once one kind has supplied images for an input, the other kind's copy
// of the same prompt is skipped.
func (r *reader) collectHuman(raw json.RawMessage, ent entity, at *time.Time, kind string) {
	if !ent.human {
		return
	}
	key := ent.source.ChildSessionID + "\x00" + ent.source.TurnID
	if prior, ok := r.humanKinds[key]; ok && prior != kind {
		return
	}
	before := len(r.result.Images)
	r.collect(raw, ent.source, at, 0)
	if len(r.result.Images) > before {
		r.humanKinds[key] = kind
	}
}

func (r *reader) line(rd *bufio.Reader) ([]byte, bool, error) {
	var out []byte
	oversized := false
	for {
		part, err := rd.ReadSlice('\n')
		r.scanned += int64(len(part))
		if r.scanned > r.limits.MaxScanBytes {
			return nil, true, errors.New("scan limit")
		}
		if len(out)+len(part) > r.limits.MaxLineBytes {
			oversized = true
			out = nil
		}
		if !oversized {
			out = append(out, part...)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return out, oversized, err
		}
	}
}

func (r *reader) claude(raw []byte, index *fileIndex) {
	if !bytes.Contains(raw, []byte(`"image"`)) {
		return
	}
	var record struct {
		Type         string          `json:"type"`
		UUID         string          `json:"uuid"`
		Timestamp    *time.Time      `json:"timestamp"`
		IsMeta       bool            `json:"isMeta"`
		PromptSource string          `json:"promptSource"`
		Origin       json.RawMessage `json:"origin"`
		Message      struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &record) != nil {
		r.note("malformed native image record")
		return
	}
	if record.Type != "user" {
		return
	}
	var blocks []json.RawMessage
	if json.Unmarshal(record.Message.Content, &blocks) != nil {
		return
	}
	pasted := ordinaryPastedEnvelope(record.IsMeta, record.PromptSource, record.Origin, blocks)
	for _, rawBlock := range blocks {
		var block struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(rawBlock, &block) != nil {
			continue
		}
		if block.Type == "tool_result" {
			if ent, ok := index.tools[block.ToolUseID]; ok {
				r.collect(block.Content, ent.source, record.Timestamp, 0)
			}
		} else if block.Type == "image" && pasted {
			if ent, ok := index.inputs[record.UUID]; ok && ent.human {
				r.collect(rawBlock, ent.source, record.Timestamp, 0)
			}
		}
	}
}

// User-role image records can also be harness messages. Apply the ordinary
// envelope check even when the mapper found human-looking text beside an image.
func ordinaryPastedEnvelope(meta bool, promptSource string, origin json.RawMessage, blocks []json.RawMessage) bool {
	if meta || !ordinaryHumanOrigin(origin) || (promptSource != "" && promptSource != "typed") {
		return false
	}
	for _, raw := range blocks {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &block) != nil || (block.Type != "text" && block.Type != "image") {
			return false
		}
		text := strings.TrimSpace(block.Text)
		if strings.HasPrefix(text, "<task-notification") || strings.HasPrefix(text, "<agent") || strings.HasPrefix(text, "<teammate-message") {
			return false
		}
	}
	return true
}

func ordinaryHumanOrigin(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return true
	}
	var origin struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(raw, &origin) == nil && origin.Kind == "human"
}

type pendingCodex struct {
	turn   string
	blocks json.RawMessage
	at     *time.Time
}

// codexTurn mirrors the mapper's turn tracking: the open turn, and the latest
// input written before a task_started, which belongs to the turn it opens.
type codexTurn struct {
	open    string
	pending *entity
}

func (r *reader) codex(raw []byte, index *fileIndex, line int, activeTurn *codexTurn) (*pendingCodex, string, entity) {
	var record struct {
		Type      string     `json:"type"`
		Timestamp *time.Time `json:"timestamp"`
		Payload   struct {
			Type     string          `json:"type"`
			Role     string          `json:"role"`
			CallID   string          `json:"call_id"`
			TurnID   string          `json:"turn_id"`
			Content  json.RawMessage `json:"content"`
			Output   json.RawMessage `json:"output"`
			Item     json.RawMessage `json:"item"`
			Metadata struct {
				TurnID string `json:"turn_id"`
			} `json:"internal_chat_message_metadata_passthrough"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &record) != nil {
		if bytes.Contains(raw, []byte("image")) {
			r.note("malformed native image record")
		}
		return nil, "", entity{}
	}
	p := record.Payload
	if record.Type == "event_msg" {
		switch p.Type {
		case "task_started":
			if p.TurnID != "" {
				activeTurn.open = p.TurnID
				if pending := activeTurn.pending; pending != nil {
					activeTurn.pending = nil
					return nil, p.TurnID, *pending
				}
			}
		case "task_complete", "turn_aborted":
			activeTurn.open = ""
		}
	}
	if ent, ok := index.inputLines[line]; ok {
		if record.Type == "event_msg" && p.Type == "item_completed" && bytes.Contains(p.Item, []byte("image")) {
			// Newer Codex versions may retain a pasted image only in the
			// UserMessage item the mapper turned into this human input.
			var item struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(p.Item, &item) == nil && item.Type == "UserMessage" {
				r.collectHuman(item.Content, ent, record.Timestamp, codexItemKind)
			}
		}
		turn := p.TurnID
		if turn == "" && record.Type == "event_msg" && (p.Type == "user_message" || p.Type == "item_completed") {
			if activeTurn.open == "" {
				pending := ent
				activeTurn.pending = &pending
				return nil, "", entity{}
			}
			turn = activeTurn.open
		}
		if turn != "" {
			return nil, turn, ent
		}
	}
	if record.Type != "response_item" {
		return nil, "", entity{}
	}
	switch p.Type {
	case "function_call_output", "custom_tool_call_output":
		if ent, ok := index.tools[p.CallID]; ok {
			r.collect(p.Output, ent.source, record.Timestamp, 0)
		}
	case "message":
		if p.Role == "user" && p.Metadata.TurnID != "" && bytes.Contains(p.Content, []byte("image")) {
			return &pendingCodex{p.Metadata.TurnID, p.Content, record.Timestamp}, "", entity{}
		}
	}
	return nil, "", entity{}
}

// collect walks recognized structured containers, not arbitrary text fields.
func (r *reader) collect(raw json.RawMessage, source Source, at *time.Time, depth int) {
	if depth > 12 || len(raw) == 0 || len(r.result.Images) >= r.limits.MaxImages {
		return
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return
	}
	switch raw[0] {
	case '[':
		var blocks []json.RawMessage
		if json.Unmarshal(raw, &blocks) != nil {
			return
		}
		for _, block := range blocks {
			r.collect(block, source, at, depth+1)
		}
	case '"':
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return
		}
		if strings.HasPrefix(text, "data:image/") {
			r.add(text, "", source, at)
			return
		}
		if strings.HasPrefix(strings.TrimSpace(text), "{") || strings.HasPrefix(strings.TrimSpace(text), "[") {
			r.collect(json.RawMessage(text), source, at, depth+1)
		}
	case '{':
		var block struct {
			Type      string          `json:"type"`
			Data      string          `json:"data"`
			MimeType  string          `json:"mimeType"`
			MediaType string          `json:"media_type"`
			ImageURL  json.RawMessage `json:"image_url"`
			Source    struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
			Content json.RawMessage `json:"content"`
			Output  json.RawMessage `json:"output"`
		}
		if json.Unmarshal(raw, &block) != nil {
			return
		}
		switch block.Type {
		case "image", "input_image", "output_image", "image_url":
			switch {
			case block.Source.Type == "base64":
				r.add(block.Source.Data, block.Source.MediaType, source, at)
			case block.Data != "":
				mime := block.MimeType
				if mime == "" {
					mime = block.MediaType
				}
				r.add(block.Data, mime, source, at)
			case len(block.ImageURL) > 0:
				var url string
				if json.Unmarshal(block.ImageURL, &url) == nil {
					r.add(url, "", source, at)
				} else {
					var obj struct {
						URL string `json:"url"`
					}
					if json.Unmarshal(block.ImageURL, &obj) == nil {
						r.add(obj.URL, "", source, at)
					}
				}
			default:
				r.note("retained image payload unavailable")
			}
		default:
			r.collect(block.Content, source, at, depth+1)
			r.collect(block.Output, source, at, depth+1)
		}
	}
}

func (r *reader) add(encoded, mime string, source Source, at *time.Time) {
	key := source.ChildSessionID + "\x00" + source.TurnID + "\x00" + source.ToolUseID
	r.positions[key]++
	source.Position = r.positions[key]
	if strings.HasPrefix(encoded, "data:") {
		header, data, ok := strings.Cut(encoded, ",")
		if !ok {
			r.note("invalid retained image payload")
			return
		}
		contentType, ok := strings.CutSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		if !ok {
			r.note("unsupported retained image encoding")
			return
		}
		mime, encoded = contentType, data
	}
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		r.note("retained image format unsupported")
		return
	}
	if int64(base64.StdEncoding.DecodedLen(len(encoded))) > r.limits.MaxImageBytes+2 {
		r.note("retained image exceeds byte limit")
		return
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		r.note("invalid retained image payload")
		return
	}
	if int64(len(data)) > r.limits.MaxImageBytes || r.total+int64(len(data)) > r.limits.MaxTotalBytes || len(r.result.Images) >= r.limits.MaxImages {
		r.note("session image budget reached")
		return
	}
	if at != nil {
		source.Timestamp = at
	}
	r.total += int64(len(data))
	r.result.Images = append(r.result.Images, Image{Bytes: data, MediaType: mime, Source: source})
}
