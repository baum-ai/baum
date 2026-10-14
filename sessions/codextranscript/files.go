package codextranscript

import (
	"fmt"
	"io/fs"
	"sort"

	"github.com/baumai/baum/sessions/traceprocessing"
)

// Tree is the explicitly listed, fully decoded rollout set for one session.
type Tree struct {
	Root        *File
	Children    []*File
	ForkSources []*File
	byThread    map[string]*File
}

func SingleFileTree(file *File) (*Tree, error) {
	if file == nil || file.Meta == nil {
		return nil, fmt.Errorf("constructing Codex transcript tree: file has no session metadata")
	}
	class, err := ClassifyThread(file.Lines)
	if err != nil {
		return nil, fmt.Errorf("constructing Codex transcript tree: %w", err)
	}
	if class != ThreadRoot {
		return nil, fmt.Errorf("constructing Codex transcript tree: %s is not a root rollout", file.Path)
	}
	return &Tree{Root: file, byThread: map[string]*File{file.Meta.ID: file}}, nil
}

// Files returns the rollouts mapped into sessions, root first. Fork sources
// supply inherited-history boundaries but are not sessions of this tree.
func (t *Tree) Files() []*File {
	if t == nil || t.Root == nil {
		return nil
	}
	out := make([]*File, 0, len(t.Children)+1)
	out = append(out, t.Root)
	return append(out, t.Children...)
}

func (t *Tree) Rollout(threadID string) *File {
	if t == nil {
		return nil
	}
	return t.byThread[threadID]
}

type FileSources = traceprocessing.FileSources

func DecodeLocalFiles[T FileSources](files T) (*Tree, error) {
	return DecodeFiles(traceprocessing.LocalFiles{}, files)
}

func DecodeFiles[T FileSources](fsys fs.FS, files T) (*Tree, error) {
	tree := &Tree{byThread: map[string]*File{}}
	for i := range files.Len() {
		name, path, kind := files.At(i)
		file, err := readFile(fsys, path, path)
		if err != nil {
			return nil, err
		}
		if other := tree.byThread[file.Meta.ID]; other != nil {
			return nil, fmt.Errorf("rollouts %s and %s both report thread id %s", other.Path, file.Path, file.Meta.ID)
		}
		tree.byThread[file.Meta.ID] = file
		switch kind {
		case "transcript":
			if tree.Root != nil {
				return nil, fmt.Errorf("session names two root rollouts, %s and %s", tree.Root.Path, file.Path)
			}
			tree.Root = file
		case "sub_transcript":
			tree.Children = append(tree.Children, file)
		case "fork_source":
			tree.ForkSources = append(tree.ForkSources, file)
		default:
			return nil, fmt.Errorf("session names %s with unsupported kind %q", name, kind)
		}
	}
	if tree.Root == nil {
		return nil, fmt.Errorf("session names no root rollout")
	}
	if class, err := ClassifyThread(tree.Root.Lines); err != nil || class != ThreadRoot {
		return nil, fmt.Errorf("root rollout %s is not a root thread", tree.Root.Path)
	}
	for _, child := range tree.Children {
		if class, err := ClassifyThread(child.Lines); err != nil || class != ThreadChild {
			return nil, fmt.Errorf("child rollout %s is not a collaboration child", child.Path)
		}
	}
	sort.Slice(tree.Children, func(i, j int) bool { return tree.Children[i].Meta.ID < tree.Children[j].Meta.ID })
	return tree, nil
}
