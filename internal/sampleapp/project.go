package sampleapp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The sample project is a small notes library on GitHub as example/notes,
// worked on by the person ("sam") and two teammates. Its history is written
// fresh each time, dated back from now, so it always looks current.
const (
	repository = "example/notes"
	me         = "sam"
	myName     = "Sam Lee"
)

// person is someone who commits to the sample project.
type person struct{ login, name string }

var (
	sam   = person{me, myName}
	alex  = person{"alex", "Alex Kim"}
	riley = person{"riley", "Riley Park"}
)

// commit is one commit of the sample project: who wrote it, how long ago,
// and the files it writes (an empty text removes the file).
type commit struct {
	author  person
	ago     time.Duration
	message string
	files   map[string]string
}

// pull is one of the sample project's pull requests and the commits of its
// branch, which starts from main.
type pull struct {
	number          int
	branch, title   string
	body            string
	author          person
	opened, updated time.Duration
	commits         []commit
}

const day = 24 * time.Hour

var baseFiles = map[string]string{
	"README.md": `# notes

A small library for keeping notes: add them, list them newest first and
search them.

` + "```go" + `
var s notes.Store
s.Add(notes.Note{Title: "Groceries", Body: "Milk, eggs"})
found := s.Search("milk")
` + "```" + `
`,
	"go.mod": "module example.com/notes\n\ngo 1.24\n",
	"note.go": `package notes

import "time"

// Note is one note: a title, its text and when it was written.
type Note struct {
	ID      int
	Title   string
	Body    string
	Created time.Time
}
`,
	"store.go": `package notes

// Store keeps notes in memory.
type Store struct {
	notes  []Note
	nextID int
}

// Add saves a note and returns it with its ID.
func (s *Store) Add(n Note) Note {
	s.nextID++
	n.ID = s.nextID
	s.notes = append(s.notes, n)
	return n
}

// List returns every note in the order they were added.
func (s *Store) List() []Note {
	return append([]Note(nil), s.notes...)
}
`,
	"search.go": `package notes

import "strings"

// Search returns the notes whose title or text contains query, ignoring case.
func (s *Store) Search(query string) []Note {
	query = strings.ToLower(query)
	var found []Note
	for _, n := range s.List() {
		if strings.Contains(strings.ToLower(n.Title+" "+n.Body), query) {
			found = append(found, n)
		}
	}
	return found
}
`,
	"store_test.go": `package notes

import "testing"

func TestAddGivesEachNoteAnID(t *testing.T) {
	var s Store
	first, second := s.Add(Note{Title: "a"}), s.Add(Note{Title: "b"})
	if first.ID == second.ID {
		t.Fatalf("both notes got ID %d", first.ID)
	}
}

func TestSearchIgnoresCase(t *testing.T) {
	var s Store
	s.Add(Note{Title: "Groceries", Body: "Milk"})
	if got := len(s.Search("MILK")); got != 1 {
		t.Fatalf("found %d notes, want 1", got)
	}
}
`,
}

// newestFirst is pull request 9, merged: the list shows the newest notes
// first.
var newestFirst = pull{number: 9, branch: "newest-first", title: "List the newest notes first", author: sam, opened: 9 * day, updated: 8 * day,
	body: "Most people look for what they wrote last, so the list starts there.",
	commits: []commit{{sam, 9 * day, "List the newest notes first", map[string]string{
		"store.go": `package notes

import "sort"

// Store keeps notes in memory.
type Store struct {
	notes  []Note
	nextID int
}

// Add saves a note and returns it with its ID.
func (s *Store) Add(n Note) Note {
	s.nextID++
	n.ID = s.nextID
	s.notes = append(s.notes, n)
	return n
}

// List returns every note, newest first.
func (s *Store) List() []Note {
	list := append([]Note(nil), s.notes...)
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	return list
}
`}}}}

// The open pull requests, each a branch from main after pull request 9.
var (
	tags = pull{number: 12, branch: "tags", title: "Add tags to notes", author: alex, opened: 2 * day, updated: 5 * time.Hour,
		body: "Notes can carry tags, and the list can be filtered by one. Tags are cleaned when a note is saved.",
		commits: []commit{
			{alex, 2 * day, "Add tags to notes", map[string]string{
				"note.go": `package notes

import "time"

// Note is one note: a title, its text, its tags and when it was written.
type Note struct {
	ID      int
	Title   string
	Body    string
	Tags    []string
	Created time.Time
}
`,
				"tags.go": `package notes

import (
	"slices"
	"strings"
)

// CleanTags trims and lowercases tags and drops empty and repeated ones.
func CleanTags(tags []string) []string {
	var clean []string
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && !slices.Contains(clean, tag) {
			clean = append(clean, tag)
		}
	}
	return clean
}

// ByTag returns the notes that carry tag, newest first.
func (s *Store) ByTag(tag string) []Note {
	tag = strings.ToLower(strings.TrimSpace(tag))
	var found []Note
	for _, n := range s.List() {
		if slices.Contains(n.Tags, tag) {
			found = append(found, n)
		}
	}
	return found
}
`,
				"store.go": `package notes

import "sort"

// Store keeps notes in memory.
type Store struct {
	notes  []Note
	nextID int
}

// Add saves a note, with its tags cleaned, and returns it with its ID.
func (s *Store) Add(n Note) Note {
	s.nextID++
	n.ID = s.nextID
	n.Tags = CleanTags(n.Tags)
	s.notes = append(s.notes, n)
	return n
}

// List returns every note, newest first.
func (s *Store) List() []Note {
	list := append([]Note(nil), s.notes...)
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	return list
}
`}},
			{alex, 5 * time.Hour, "Test tags and mention them in the README", map[string]string{
				"tags_test.go": `package notes

import (
	"slices"
	"testing"
)

func TestCleanTags(t *testing.T) {
	got := CleanTags([]string{" Work ", "work", "", "Home"})
	if want := []string{"work", "home"}; !slices.Equal(got, want) {
		t.Fatalf("CleanTags = %q, want %q", got, want)
	}
}

func TestByTag(t *testing.T) {
	var s Store
	s.Add(Note{Title: "Plan", Tags: []string{"Work"}})
	s.Add(Note{Title: "Shopping", Tags: []string{"home"}})
	if got := s.ByTag("work"); len(got) != 1 || got[0].Title != "Plan" {
		t.Fatalf("ByTag(work) = %v", got)
	}
}
`,
				"README.md": `# notes

A small library for keeping notes: add them, list them newest first, tag
them and search them.

` + "```go" + `
var s notes.Store
s.Add(notes.Note{Title: "Groceries", Body: "Milk, eggs", Tags: []string{"home"}})
found := s.Search("milk")
home := s.ByTag("home")
` + "```" + `

Tags are trimmed and lowercased when a note is saved, so "Home" and "home"
are the same tag.
`}},
		}}
	accents = pull{number: 14, branch: "search-accents", title: "Match search without accents", author: sam, opened: day, updated: 3 * time.Hour,
		body: "Searching for \"cafe\" now finds notes that say \"café\".",
		commits: []commit{{sam, 26 * time.Hour, "Match search without accents", map[string]string{
			"search.go": `package notes

import "strings"

// Search returns the notes whose title or text contains query, ignoring
// case and accents.
func (s *Store) Search(query string) []Note {
	query = fold(query)
	var found []Note
	for _, n := range s.List() {
		if strings.Contains(fold(n.Title+" "+n.Body), query) {
			found = append(found, n)
		}
	}
	return found
}

// accents maps the accented Latin letters notes use most to plain ones.
var accents = strings.NewReplacer("á", "a", "à", "a", "â", "a", "ä", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ï", "i", "ó", "o", "ô", "o", "ö", "o", "ú", "u", "ü", "u", "ç", "c", "ñ", "n")

// fold lowercases s and takes the accents off its letters, so "Café"
// matches "cafe".
func fold(s string) string { return accents.Replace(strings.ToLower(s)) }
`,
			"search_test.go": `package notes

import "testing"

func TestSearchIgnoresAccents(t *testing.T) {
	var s Store
	s.Add(Note{Title: "Café on Main Street"})
	if got := len(s.Search("cafe")); got != 1 {
		t.Fatalf("found %d notes, want 1", got)
	}
}
`}}}}
	readme = pull{number: 15, branch: "readme", title: "Explain search in the README", author: sam, opened: 4 * time.Hour, updated: 40 * time.Minute,
		body: "A short section on how search matches.",
		commits: []commit{{sam, 40 * time.Minute, "Explain search in the README", map[string]string{
			"README.md": baseFiles["README.md"] + "\nSearch ignores case, so \"milk\" finds \"Milk\".\n"}}}}
	sortByEdit = pull{number: 16, branch: "sort-by-edit", title: "Sort notes by last edit", author: sam, opened: 6 * time.Hour, updated: 20 * time.Minute,
		body: "Notes edited recently move to the top of the list.",
		commits: []commit{{sam, 20 * time.Minute, "Sort notes by last edit", map[string]string{
			"note.go": `package notes

import "time"

// Note is one note: a title, its text and when it was written and edited.
type Note struct {
	ID      int
	Title   string
	Body    string
	Created time.Time
	Edited  time.Time
}
`}}}}
	openPulls = []pull{tags, accents, readme, sortByEdit}
)

// project is the sample project's clone and the commits that matter.
type project struct {
	dir string
	// main is main's head; heads are each pull request's.
	main  string
	heads map[int]string
	// worktrees are where the person's agent sessions work, by branch.
	worktrees map[string]string
}

// writeProject writes the sample project's history into dir, with a
// worktree beside it for each branch the person's sessions work on.
func writeProject(ctx context.Context, dir string, now time.Time) (project, error) {
	p := project{dir: dir, heads: map[int]string{}, worktrees: map[string]string{}}
	git := func(at time.Time, who person, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		date := at.Format(time.RFC3339)
		email := who.login + "@example.com"
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME="+who.name, "GIT_AUTHOR_EMAIL="+email, "GIT_AUTHOR_DATE="+date,
			"GIT_COMMITTER_NAME="+who.name, "GIT_COMMITTER_EMAIL="+email, "GIT_COMMITTER_DATE="+date)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	write := func(c commit) (string, error) {
		for name, text := range c.files {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				return "", err
			}
		}
		at := now.Add(-c.ago)
		if _, err := git(at, c.author, "add", "-A"); err != nil {
			return "", err
		}
		if _, err := git(at, c.author, "commit", "-q", "-m", c.message); err != nil {
			return "", err
		}
		return git(at, c.author, "rev-parse", "HEAD")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return p, err
	}
	start := now.Add(-20 * day)
	if _, err := git(start, sam, "init", "-q", "-b", "main"); err != nil {
		return p, err
	}
	if _, err := git(start, sam, "remote", "add", "origin", "https://github.com/"+repository); err != nil {
		return p, err
	}
	if _, err := write(commit{sam, 20 * day, "Start the notes library", baseFiles}); err != nil {
		return p, err
	}
	for _, c := range newestFirst.commits {
		head, err := write(c)
		if err != nil {
			return p, err
		}
		p.main, p.heads[newestFirst.number] = head, head
	}
	for _, pr := range openPulls {
		if _, err := git(now, sam, "checkout", "-q", "-b", pr.branch, "main"); err != nil {
			return p, err
		}
		for _, c := range pr.commits {
			head, err := write(c)
			if err != nil {
				return p, err
			}
			p.heads[pr.number] = head
		}
	}
	if _, err := git(now, sam, "checkout", "-q", "main"); err != nil {
		return p, err
	}
	for _, branch := range []string{sortByEdit.branch, readme.branch} {
		worktree := filepath.Join(filepath.Dir(dir), "notes-"+branch)
		if _, err := git(now, sam, "worktree", "add", "-q", worktree, branch); err != nil {
			return p, err
		}
		p.worktrees[branch] = worktree
	}
	return p, nil
}
