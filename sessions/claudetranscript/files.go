package claudetranscript

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/baum-ai/baum/sessions/traceprocessing"
)

// File is one transcript: the main thread, or one subagent.
type File struct {
	Path      string
	SessionID string
	AgentID   string
	Meta      *AgentMeta
	Lines     []Line
	// TornFinalLine is true when the last line was a partial write, which happens
	// while a session is still running.
	TornFinalLine bool
}

// Main reports whether this is the main thread rather than a subagent.
func (f *File) Main() bool { return f.AgentID == "" }

// AgentMeta is `subagents/agent-<A>.meta.json`, the primary spawn link.
// ToolUseID names the spawning call directly.
type AgentMeta struct {
	AgentType     string `json:"agentType"`
	Description   string `json:"description"`
	ToolUseID     string `json:"toolUseId"`
	SpawnDepth    int    `json:"spawnDepth"`
	ParentAgentID string `json:"parentAgentId"`
	Model         string `json:"model"`
}

type Tree struct {
	Main      *File
	Subagents []*File

	byToolUse map[string]*File
}

// SingleFileTree constructs the tree represented by one already-decoded main transcript.
func SingleFileTree(file *File) (*Tree, error) {
	if file == nil {
		return nil, fmt.Errorf("constructing Claude transcript tree: nil file")
	}
	if !file.Main() {
		return nil, fmt.Errorf("constructing Claude transcript tree: %s is a subagent", file.Path)
	}
	if file.SessionID == "" {
		return nil, fmt.Errorf("constructing Claude transcript tree: %s names no session", file.Path)
	}
	return &Tree{
		Main: file, byToolUse: map[string]*File{},
	}, nil
}

// SubagentForToolUse returns the transcript a tool call spawned, or nil.
func (t *Tree) SubagentForToolUse(toolUseID string) *File {
	if toolUseID == "" {
		return nil
	}
	return t.byToolUse[toolUseID]
}

// Files is every transcript in the tree, main first.
func (t *Tree) Files() []*File {
	out := make([]*File, 0, len(t.Subagents)+1)
	if t.Main != nil {
		out = append(out, t.Main)
	}
	return append(out, t.Subagents...)
}

type sourceFile struct {
	Name string
	Path string
	Kind string
}

type FileSources = traceprocessing.FileSources

// DecodeFiles decodes the explicitly listed session tree.
func DecodeFiles[T FileSources](fsys fs.FS, files T) (*Tree, error) {
	return decodeFiles(fsys, files)
}

// DecodeLocalFiles decodes an explicitly listed set of local files.
func DecodeLocalFiles[T FileSources](files T) (*Tree, error) {
	return decodeFiles(traceprocessing.LocalFiles{}, files)
}

// sessionSources is the classified view of an explicitly listed session tree.
type sessionSources struct {
	main        sourceFile
	subagents   map[string]sourceFile
	metaByAgent map[string]sourceFile
	agentIDs    []string
}

func decodeFiles[T FileSources](fsys fs.FS, files T) (*Tree, error) {
	sources, err := classifyFileSources(files)
	if err != nil {
		return nil, err
	}
	main, err := readFile(fsys, sources.main.Path, sources.main.Name, sources.main.Path, "")
	if err != nil {
		return nil, err
	}
	tree := &Tree{Main: main, byToolUse: map[string]*File{}}
	if err := attachSubagents(fsys, tree, sources); err != nil {
		return nil, err
	}
	return tree, nil
}

// classifyFileSources buckets listed files by kind and checks the set is consistent.
func classifyFileSources[T FileSources](files T) (sessionSources, error) {
	sources := sessionSources{
		subagents:   map[string]sourceFile{},
		metaByAgent: map[string]sourceFile{},
		agentIDs:    make([]string, 0, files.Len()),
	}
	for i := range files.Len() {
		name, filePath, kind := files.At(i)
		source := sourceFile{Name: name, Path: filePath, Kind: kind}
		switch kind {
		case "sub_transcript":
			agentID, err := agentIDFromName(name, ".jsonl")
			if err != nil {
				return sessionSources{}, err
			}
			if _, dup := sources.subagents[agentID]; dup {
				return sessionSources{}, fmt.Errorf("session names subagent %s twice", agentID)
			}
			sources.subagents[agentID] = source
			sources.agentIDs = append(sources.agentIDs, agentID)
		case "sub_metadata":
			agentID, err := agentIDFromName(name, ".meta.json")
			if err != nil {
				return sessionSources{}, err
			}
			if _, dup := sources.metaByAgent[agentID]; dup {
				return sessionSources{}, fmt.Errorf("session names sidecar for subagent %s twice", agentID)
			}
			sources.metaByAgent[agentID] = source
		case "transcript":
			if sources.main.Name != "" {
				return sessionSources{}, fmt.Errorf("session names two main transcripts, %s and %s", sources.main.Name, name)
			}
			sources.main = source
		default:
			return sessionSources{}, fmt.Errorf("session names %s with unsupported kind %q", name, kind)
		}
	}
	if sources.main.Name == "" {
		return sessionSources{}, fmt.Errorf("session names no main transcript")
	}
	for agentID := range sources.metaByAgent {
		if _, known := sources.subagents[agentID]; !known {
			return sessionSources{}, fmt.Errorf("session names a sidecar for subagent %s but not its transcript", agentID)
		}
	}
	slices.Sort(sources.agentIDs)
	return sources, nil
}

// attachSubagents reads each subagent transcript (and optional sidecar) into tree.
func attachSubagents(fsys fs.FS, tree *Tree, sources sessionSources) error {
	for _, agentID := range sources.agentIDs {
		source := sources.subagents[agentID]
		file, err := readFile(fsys, source.Path, source.Name, source.Path, agentID)
		if err != nil {
			return err
		}
		if meta, known := sources.metaByAgent[agentID]; known {
			file.Meta, err = readAgentMeta(fsys, meta.Path, meta.Path)
			if err != nil {
				return err
			}
		}
		if err := indexSubagent(tree, file, source.Path); err != nil {
			return err
		}
	}
	return nil
}

// indexSubagent checks a subagent against its parent and records it on the tree.
func indexSubagent(tree *Tree, file *File, sourcePath string) error {
	if file.SessionID != "" && tree.Main.SessionID != "" && file.SessionID != tree.Main.SessionID {
		return fmt.Errorf("subagent %s reports session %s, but its parent is %s",
			sourcePath, file.SessionID, tree.Main.SessionID)
	}
	tree.Subagents = append(tree.Subagents, file)
	if file.Meta == nil || file.Meta.ToolUseID == "" {
		return nil
	}
	if other, dup := tree.byToolUse[file.Meta.ToolUseID]; dup {
		return fmt.Errorf("subagents %s and %s both claim to be spawned by tool use %s",
			other.Path, sourcePath, file.Meta.ToolUseID)
	}
	tree.byToolUse[file.Meta.ToolUseID] = file
	return nil
}

func agentIDFromName(name, suffix string) (string, error) {
	if !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, suffix) {
		return "", fmt.Errorf("subagent file %s does not match agent-<id>%s", name, suffix)
	}
	agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), suffix)
	if agentID == "" {
		return "", fmt.Errorf("subagent file %s names no agent", name)
	}
	return agentID, nil
}

// readFile reads one transcript out of fsys.
func readFile(fsys fs.FS, path, name, label, agentID string) (*File, error) {
	handle, err := fsys.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening transcript %s: %w", label, err)
	}
	defer func() { _ = handle.Close() }()

	return decodeFile(handle, name, label, agentID)
}

// DecodeSubagent reads one subagent transcript that arrived outside a session
// directory, under the same checks as one listed beside its parent. The caller
// supplies the spawn link with Meta and adds the file with AddSubagent.
func DecodeSubagent(r io.Reader, label, agentID string) (*File, error) {
	if agentID == "" {
		return nil, fmt.Errorf("subagent transcript %s names no agent", label)
	}
	return decodeFile(r, path.Base(label), label, agentID)
}

// AddSubagent records a decoded subagent on the tree, checking it against its
// parent exactly as a listed one is.
func (t *Tree) AddSubagent(file *File) error {
	if file == nil || file.Main() {
		return fmt.Errorf("adding subagent: not a subagent transcript")
	}
	for _, existing := range t.Subagents {
		if existing.AgentID == file.AgentID {
			return fmt.Errorf("session names subagent %s twice", file.AgentID)
		}
	}
	return indexSubagent(t, file, file.Path)
}

func decodeFile(r io.Reader, name, label, agentID string) (*File, error) {
	lines, torn, err := Decode(r)
	if err != nil {
		return nil, fmt.Errorf("reading transcript %s: %w", label, err)
	}

	file := &File{Path: label, AgentID: agentID, Lines: lines, TornFinalLine: torn}
	if agentID == "" {
		if stem := strings.TrimSuffix(name, ".jsonl"); isUUID(stem) {
			file.SessionID = stem
		}
	}

	for _, line := range lines {
		if line.SessionID != "" && file.SessionID == "" {
			file.SessionID = line.SessionID
		}
		if agentID == "" || !line.Envelope() {
			continue
		}
		if !line.IsSidechain {
			return nil, fmt.Errorf("%s line %d is not marked as a sidechain", label, line.Ordinal)
		}
		if line.AgentID != agentID {
			return nil, fmt.Errorf("%s line %d reports agent %q, want %q", label, line.Ordinal, line.AgentID, agentID)
		}
	}

	if file.SessionID == "" {
		return nil, fmt.Errorf("transcript %s names no session", label)
	}
	return file, nil
}

// readAgentMeta reads one explicitly listed sidecar describing a subagent.
func readAgentMeta(fsys fs.FS, path, label string) (*AgentMeta, error) {
	raw, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", label, err)
	}
	var meta AgentMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("reading %s: %w", label, err)
	}
	return &meta, nil
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidShape.MatchString(s) }
