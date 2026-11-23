package seeddata

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"time"
)

// gitRepo is a written repository: the commit main points at and each pull
// request's commits, one per version, oldest first.
type gitRepo struct {
	dir   string
	main  string
	heads map[int][]string
}

// commitPlan is one commit in the fast-import stream.
type commitPlan struct {
	ref     string
	from    string // a mark, ":n"
	author  string
	at      time.Time
	message string
	files   map[string][]string // nil content removes the file
}

// writeRepository writes r's history into dir with git fast-import: main,
// then a branch per pull request with a commit per version. Every date and
// name is fixed, so the commits' SHAs depend on the seed alone.
func writeRepository(ctx context.Context, o Options, r *repoPlan, dir string) (gitRepo, error) {
	out := gitRepo{dir: dir, heads: map[int][]string{}}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return out, err
	}
	if _, err := git(ctx, dir, nil, "init", "-q", "-b", "main"); err != nil {
		return out, err
	}
	if _, err := git(ctx, dir, nil, "remote", "add", "origin", "https://github.com/"+r.identity()); err != nil {
		return out, err
	}
	rng := rand.New(rand.NewPCG(uint64(o.Seed), uint64(r.index)+1))
	mainFiles := map[string][]string{"README.md": {"# " + r.name, "", "The " + r.name + " service of " + r.owner + "."}}
	for _, f := range r.files {
		mainFiles[f.path] = fileLines(rng, f.path, f.lines, "main")
	}
	var stream bytes.Buffer
	mark := 0
	start := o.Epoch.Add(-90 * 24 * time.Hour)
	mark++
	mainMark := fmt.Sprintf(":%d", mark)
	writeCommit(&stream, mark, commitPlan{ref: "refs/heads/main", author: people[r.index%len(people)], at: start,
		message: "Start " + r.name, files: mainFiles})
	marks := map[int][]int{}
	for _, pr := range r.pullRequests {
		prRng := rand.New(rand.NewPCG(uint64(o.Seed), uint64(r.index)<<32|uint64(pr.number)))
		files := map[string][]string{}
		for name, lines := range mainFiles {
			files[name] = lines
		}
		from := mainMark
		for v := 1; v <= pr.versions; v++ {
			var changed map[string][]string
			switch {
			case v == 1 && pr.big:
				changed = bigChange(prRng, files, pr)
			case v == 1:
				changed = change(prRng, files, pr, pr.lines)
			default:
				changed = fixup(prRng, files, pr, v)
			}
			for name, lines := range changed {
				files[name] = lines
			}
			at := o.Epoch.Add(-time.Duration(pr.updated)*time.Minute - time.Duration(pr.versions-v)*7*time.Hour)
			mark++
			author := pr.author
			message := pr.title
			if v > 1 {
				message = fmt.Sprintf("Address review comments (%d)", v-1)
			}
			writeCommit(&stream, mark, commitPlan{ref: "refs/heads/" + pr.branch, from: from, author: author, at: at, message: message, files: changed})
			from = fmt.Sprintf(":%d", mark)
			marks[pr.number] = append(marks[pr.number], mark)
		}
	}
	marksFile := path.Join(dir, ".git", "seeddata-marks")
	if _, err := git(ctx, dir, &stream, "fast-import", "--quiet", "--export-marks="+marksFile); err != nil {
		return out, err
	}
	raw, err := os.ReadFile(marksFile)
	if err != nil {
		return out, err
	}
	_ = os.Remove(marksFile)
	shas := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if m, sha, ok := strings.Cut(line, " "); ok {
			shas[m] = sha
		}
	}
	out.main = shas[mainMark]
	for number, list := range marks {
		for _, m := range list {
			out.heads[number] = append(out.heads[number], shas[fmt.Sprintf(":%d", m)])
		}
	}
	if _, err := git(ctx, dir, nil, "reset", "-q", "--hard", "main"); err != nil {
		return out, err
	}
	return out, nil
}

func writeCommit(w *bytes.Buffer, mark int, c commitPlan) {
	email := strings.TrimSuffix(c.author, "[bot]") + "@users.noreply.github.com"
	fmt.Fprintf(w, "commit %s\nmark :%d\n", c.ref, mark)
	fmt.Fprintf(w, "author %s <%s> %d +0000\n", c.author, email, c.at.Unix())
	fmt.Fprintf(w, "committer %s <%s> %d +0000\n", c.author, email, c.at.Unix())
	fmt.Fprintf(w, "data %d\n%s\n", len(c.message), c.message)
	if c.from != "" {
		fmt.Fprintf(w, "from %s\n", c.from)
	}
	names := make([]string, 0, len(c.files))
	for name := range c.files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		lines := c.files[name]
		if lines == nil {
			fmt.Fprintf(w, "D %s\n", name)
			continue
		}
		body := strings.Join(lines, "\n") + "\n"
		fmt.Fprintf(w, "M 100644 inline %s\ndata %d\n%s\n", name, len(body), body)
	}
	w.WriteString("\n")
}

// git runs git in dir with no user or system configuration, reading stdin
// when given.
func git(ctx context.Context, dir string, stdin *bytes.Buffer, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if stdin != nil {
		cmd.Stdin = stdin
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// fileLines is a file's text: plausible lines for its kind, each unique.
func fileLines(rng *rand.Rand, name string, n int, tag string) []string {
	lines := make([]string, 0, n)
	switch path.Ext(name) {
	case ".go":
		lines = append(lines, "package "+path.Base(path.Dir(name)), "")
	case ".ts":
		lines = append(lines, "// "+name, "")
	}
	for len(lines) < n {
		lines = append(lines, codeLine(rng, name, tag, len(lines)))
	}
	return lines[:n]
}

var (
	identifiers = []string{"request", "limit", "result", "entry", "cursor", "budget", "window", "record", "token", "batch", "owner", "handler"}
	functions   = []string{"lookup", "normalize", "persist", "schedule", "render", "validate", "decode", "measure", "retry", "flush"}
)

// codeLine is one line of a file, unique within it by its tag and position.
func codeLine(rng *rand.Rand, name, tag string, i int) string {
	id := identifiers[rng.IntN(len(identifiers))]
	fn := functions[rng.IntN(len(functions))]
	switch path.Ext(name) {
	case ".md":
		return fmt.Sprintf("The %s step %s each %s before it is saved (%s %d).", fn, "checks", id, tag, i)
	case ".yaml":
		return fmt.Sprintf("%s_%s_%d: %d # %s", id, fn, i, rng.IntN(1000), tag)
	case ".ts":
		return fmt.Sprintf("export const %s%s_%d = %s(%q, %d); // %s", id, strings.ToUpper(fn[:1])+fn[1:], i, fn, id, rng.IntN(1000), tag)
	}
	switch rng.IntN(4) {
	case 0:
		return fmt.Sprintf("func %s%s%d(ctx context.Context) error { return %s(ctx, %q) } // %s", fn, strings.ToUpper(id[:1])+id[1:], i, fn, id, tag)
	case 1:
		return fmt.Sprintf("var %s%d = %s(%d) // %s", id, i, fn, rng.IntN(10000), tag)
	case 2:
		return fmt.Sprintf("// %s%d %ss the %s before the next step reads it (%s).", fn, i, fn, id, tag)
	default:
		return fmt.Sprintf("const %sLimit%d = %d // %s", id, i, rng.IntN(5000), tag)
	}
}

// change makes a pull request's first commit: edits to existing files and
// sometimes a new file, about lines changed lines in all.
func change(rng *rand.Rand, files map[string][]string, pr *prPlan, lines int) map[string][]string {
	names := make([]string, 0, len(files))
	for name := range files {
		if name != "internal/legacy/table.go" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	out := map[string][]string{}
	tag := fmt.Sprintf("pr%d", pr.number)
	left := lines
	for step := 0; left > 0 && step < 400; step++ {
		size := min(left, 1+rng.IntN(max(1, lines/2+1)))
		if step > 0 && rng.IntN(5) == 0 || len(names) == 0 {
			name := fmt.Sprintf("%s/%s_%d.go", directories[rng.IntN(len(directories))], topicWord(rng), pr.number*10+step)
			if _, ok := files[name]; ok {
				continue
			}
			out[name] = fileLines(rng, name, max(size, 3), tag)
			left -= max(size, 3)
			continue
		}
		name := names[rng.IntN(len(names))]
		current := out[name]
		if current == nil {
			current = slices.Clone(files[name])
		}
		at := min(len(current), 2+rng.IntN(max(1, len(current)-2)))
		switch op := rng.IntN(3); {
		case op == 0 || size < 2: // insert
			added := make([]string, size)
			for i := range added {
				added[i] = codeLine(rng, name, tag, step*10000+i)
			}
			current = slices.Insert(current, at, added...)
			left -= size
		case op == 1: // replace
			n := min(size/2, len(current)-at)
			if n <= 0 {
				continue
			}
			for i := 0; i < n; i++ {
				current[at+i] = codeLine(rng, name, tag, step*10000+i)
			}
			left -= 2 * n
		default: // delete
			n := min(size, len(current)-at, 30)
			if n <= 0 {
				continue
			}
			current = slices.Delete(current, at, at+n)
			left -= n
		}
		out[name] = current
	}
	return out
}

// bigChange is the large pull request's first commit: four new files of a
// thousand lines and five hundred lines of the legacy table replaced, so
// the diff is exactly o.LargeDiffLines lines (5,000 by default).
func bigChange(rng *rand.Rand, files map[string][]string, pr *prPlan) map[string][]string {
	out := map[string][]string{}
	replaced := min(pr.lines/10, 500)
	added := pr.lines - 2*replaced
	tag := fmt.Sprintf("pr%d", pr.number)
	for i := 0; added > 0; i++ {
		n := min(added, 1000)
		name := fmt.Sprintf("internal/routes/generated_%d.go", i)
		body := []string{"package routes", ""}
		for len(body) < n {
			body = append(body, fmt.Sprintf("\tregister(%q, handle%s%d) // %s", "/v2/"+identifiers[len(body)%len(identifiers)]+fmt.Sprint(len(body)), functions[len(body)%len(functions)], len(body), tag))
		}
		out[name] = body[:n]
		added -= n
	}
	table := slices.Clone(files["internal/legacy/table.go"])
	for i := 0; i < replaced; i++ {
		table[100+i] = fmt.Sprintf("\troutes[%d] = generatedRoute(%d) // %s", i, rng.IntN(100000), tag)
	}
	out["internal/legacy/table.go"] = table
	return out
}

// fixup is a later version's commit: a few lines changed in a file the
// first commit already changed, so the diff grows only a little. The large
// pull request edits a line of a file it added, keeping its size.
func fixup(rng *rand.Rand, files map[string][]string, pr *prPlan, v int) map[string][]string {
	tag := fmt.Sprintf("pr%d-v%d", pr.number, v)
	if pr.big {
		name := "internal/routes/generated_0.go"
		lines := slices.Clone(files[name])
		lines[10+v] = fmt.Sprintf("\tregister(%q, handleFixed%d) // %s", "/v2/fixed", v, tag)
		return map[string][]string{name: lines}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		if name != "internal/legacy/table.go" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	name := names[rng.IntN(len(names))]
	lines := slices.Clone(files[name])
	at := min(len(lines), 2+rng.IntN(max(1, len(lines)-2)))
	n := 1 + rng.IntN(4)
	for i := 0; i < n; i++ {
		lines = slices.Insert(lines, at, codeLine(rng, name, tag, i))
	}
	return map[string][]string{name: lines}
}
