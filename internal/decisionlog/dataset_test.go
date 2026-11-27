package decisionlog

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/repos"
)

// The dataset checks run capture, the adaptive build and the live feature
// stages on the private dataset of real sessions and pull requests (README.md,
// "Dataset checks"), with the real backend. They are skipped unless
// BAUM_REVIEW_DATASET names the dataset.
const (
	datasetEnv     = "BAUM_REVIEW_DATASET"
	datasetWorkEnv = "BAUM_DATASET_WORK"
)

type datasetRegistry struct {
	prefs      repos.Preferences
	repository string
	clone      string
}

func (r datasetRegistry) Preferences(context.Context) (repos.Preferences, error) { return r.prefs, nil }
func (r datasetRegistry) RepositorySettings(context.Context, string) (repos.Settings, bool, error) {
	return repos.Settings{}, true, nil
}
func (r datasetRegistry) ResolvePrompts(context.Context, string) (prompt.Overrides, map[prompt.ID]repos.PromptSource, error) {
	return prompt.Overrides{}, nil, nil
}
func (r datasetRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	return []repos.Repository{{Repository: r.repository, Clones: []repos.Clone{{Path: r.clone, Repository: r.repository, Available: true}}}}, nil
}

type datasetSession struct {
	Vendor string `json:"vendor"`
	Host   string `json:"host"`
	ID     string `json:"id"`
	Path   string `json:"path"`
	Parent any    `json:"parent"`
}

// dataset is the dataset prepared for a run: a writable copy of one machine's
// app database, a clone holding every commit of the mirror, and homes whose
// session stores are the dataset's.
type dataset struct {
	t          *testing.T
	root, work string
	host       string
	repository string
	clone      string
	out        string
	store      *Store
	registry   datasetRegistry
	config     config.Config
	sessions   map[string]datasetSession
}

func datasetGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func openDataset(t *testing.T) *dataset {
	t.Helper()
	root := os.Getenv(datasetEnv)
	if root == "" {
		t.Skip(datasetEnv + " is not set")
	}
	work := os.Getenv(datasetWorkEnv)
	if work == "" {
		t.Fatal(datasetWorkEnv + " must name a private working directory that outlives the run")
	}
	host, repository := os.Getenv("BAUM_DATASET_HOST"), os.Getenv("BAUM_DATASET_REPOSITORY")
	if host == "" || repository == "" {
		t.Fatal("BAUM_DATASET_HOST and BAUM_DATASET_REPOSITORY must name the dataset's machine and its owner/name repository")
	}
	d := &dataset{t: t, root: root, work: work, host: host, repository: repository, sessions: map[string]datasetSession{}}
	d.out = filepath.Join(work, "out")
	if err := os.MkdirAll(d.out, 0o700); err != nil {
		t.Fatal(err)
	}
	// The app database is copied once: later runs resume from what earlier
	// ones wrote, as the daemon would.
	database := filepath.Join(work, "state", "app.sqlite")
	if _, err := os.Stat(database); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(database), 0o700); err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(filepath.Join(root, "state", d.host, "app.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		target, err := os.OpenFile(database, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(target, source)
		if err = errors.Join(err, source.Close(), target.Close()); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if d.store, err = Open(database, OpenOptions{AbandonPending: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.store.Close() })

	// The clone has the mirror's objects, so recorded commits resolve without
	// the network; its remote is GitHub, for the refs a build asks for.
	d.clone = filepath.Join(work, "clone")
	if _, err := os.Stat(d.clone); errors.Is(err, os.ErrNotExist) {
		mirror := filepath.Join(root, "repos", strings.ReplaceAll(d.repository, "/", "-")+".git")
		datasetGit(t, work, "init", "-q", "-b", "main", d.clone)
		datasetGit(t, d.clone, "fetch", "-q", mirror, "+refs/*:refs/dataset/*")
		datasetGit(t, d.clone, "remote", "add", "origin", "https://github.com/"+d.repository+".git")
	}
	// Each home has one of the dataset's session stores at the place the
	// agents keep theirs.
	for name, target := range map[string]string{
		"claude/.claude":          filepath.Join(root, "sessions", "claude", d.host, ".claude"),
		"claude-personal/.claude": filepath.Join(root, "sessions", "claude", d.host, ".claude.personal"),
		"codex/.codex":            filepath.Join(root, "sessions", "codex", d.host),
	} {
		link := filepath.Join(work, "homes", name)
		if _, err := os.Lstat(link); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
		}
	}
	index, err := os.Open(filepath.Join(root, "index", "sessions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	lines := bufio.NewScanner(index)
	lines.Buffer(nil, 16<<20)
	for lines.Scan() {
		var s datasetSession
		if err := json.Unmarshal(lines.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		if s.Host == d.host && s.Parent == nil {
			d.sessions[agents[s.Vendor]+":"+s.ID] = s
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	// The machine's own preferences choose the backend and model, unless the
	// run names others.
	d.registry = datasetRegistry{prefs: repos.DefaultPreferences(), repository: d.repository, clone: d.clone}
	var prefs string
	if err := d.store.db.QueryRow(`SELECT value FROM preferences WHERE key='automatic'`).Scan(&prefs); err == nil {
		if err := json.Unmarshal([]byte(prefs), &d.registry.prefs); err != nil {
			t.Fatal(err)
		}
	}
	d.registry.prefs.AgentEnv = nil
	if d.config, err = config.Load(config.Overrides{}); err != nil {
		t.Fatal(err)
	}
	return d
}

// stored is the home whose session store holds the dataset's transcript at
// path, and the transcript's path inside that store.
func (d *dataset) stored(path string) (home, inside string) {
	for name, store := range map[string][2]string{
		"claude-personal": {filepath.Join(d.root, "sessions", "claude", d.host, ".claude.personal"), ".claude"},
		"claude":          {filepath.Join(d.root, "sessions", "claude", d.host, ".claude"), ".claude"},
		"codex":           {filepath.Join(d.root, "sessions", "codex", d.host), ".codex"},
	} {
		if relative, ok := strings.CutPrefix(path, store[0]+string(filepath.Separator)); ok {
			home = filepath.Join(d.work, "homes", name)
			return home, filepath.Join(home, store[1], relative)
		}
	}
	d.t.Fatalf("%s is in none of the dataset's session stores", path)
	return "", ""
}

func (d *dataset) write(name string, value any) {
	d.t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		d.t.Fatal(err)
	}
	path := filepath.Join(d.out, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		d.t.Fatal(err)
	}
	d.t.Logf("wrote %s", path)
}

func (d *dataset) log() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestDatasetCapture captures the sessions BAUM_DATASET_SESSIONS names
// (agent:id, comma separated) and writes, for each, what the store held
// before and holds after.
func TestDatasetCapture(t *testing.T) {
	d := openDataset(t)
	ctx := context.Background()
	fresh := os.Getenv("BAUM_DATASET_FRESH") != ""
	capturer := &Capturer{Store: d.store, Registry: d.registry, Config: d.config, Log: d.log()}
	for key := range strings.SplitSeq(os.Getenv("BAUM_DATASET_SESSIONS"), ",") {
		key = strings.TrimSpace(key)
		s, ok := d.sessions[key]
		if !ok {
			t.Errorf("%s is not a root session of %s in the dataset", key, d.host)
			continue
		}
		agent, id, _ := strings.Cut(key, ":")
		before, _, err := d.store.Session(ctx, agent, id)
		if err != nil {
			t.Fatal(err)
		}
		if fresh {
			// A full extraction: the head is forgotten, and saving obsoletes
			// the decisions it held.
			for _, table := range []string{"capture_source", "capture_session"} {
				if _, err := d.store.db.Exec(`DELETE FROM `+table+` WHERE agent=? AND session_id=?`, agent, id); err != nil {
					t.Fatal(err)
				}
			}
		}
		path := filepath.Join(d.root, s.Path)
		home, inside := d.stored(path)
		capturer.locate = func(_, cursor, agent, id, _ string) (livesessions.Location, error) {
			return livesessions.Locate(home, cursor, agent, id, inside)
		}
		target := Target{Agent: agent, ID: id, Path: path, Repository: d.repository, Branch: before.Branch, Ended: true}
		// The session's commit is its latest linked one, as the tracker
		// observes the head a session leaves.
		err = d.store.db.QueryRow(`SELECT sc.sha FROM session_commit sc LEFT JOIN pull_commit pc ON pc.repository=sc.repository AND pc.sha=sc.sha
 WHERE sc.repository=? AND sc.agent=? AND sc.session_id=? GROUP BY sc.sha ORDER BY MAX(COALESCE(pc.committed_at,0)) DESC, sc.sha LIMIT 1`, d.repository, agent, id).Scan(&target.HeadSHA)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		started := time.Now()
		report, captureErr := capturer.Capture(ctx, target, Options{Force: true, Backend: os.Getenv("BAUM_DATASET_BACKEND"), Model: os.Getenv("BAUM_DATASET_MODEL"),
			DebugDir: filepath.Join(d.out, "capture", id+os.Getenv("BAUM_DATASET_TAG"))})
		after, _, err := d.store.Session(ctx, agent, id)
		if err != nil {
			t.Fatal(err)
		}
		failure := ""
		if captureErr != nil {
			failure = captureErr.Error()
			t.Errorf("%s: %v", key, captureErr)
		}
		d.write(filepath.Join("capture", id+os.Getenv("BAUM_DATASET_TAG")+".json"), map[string]any{"session": key, "transcript": s.Path, "head_sha": target.HeadSHA, "report": report, "error": failure,
			"seconds": time.Since(started).Seconds(), "before": before.Decisions, "before_prompt": before.PromptVersion, "before_model": before.Model,
			"after": after.Decisions, "after_prompt": after.PromptVersion, "after_model": after.Model, "source": after.Source})
	}
}

func (d *dataset) worker() *FeatureWorker {
	client := github.NewFromConfig(d.config, github.Options{})
	return &FeatureWorker{Store: d.store, Registry: d.registry, Config: d.config, Log: d.log(),
		GitHub: func(ctx context.Context, repository github.Repository) (github.Reader, error) {
			return client.REST(ctx, repository)
		}}
}

// TestDatasetBuild runs, or resumes, an adaptive build and writes its
// checkpoint and the features it made.
func TestDatasetBuild(t *testing.T) {
	d := openDataset(t)
	ctx := context.Background()
	w := d.worker()
	if os.Getenv("BAUM_DATASET_FRESH") != "" {
		if err := d.store.ArchiveFeatureBuild(ctx, d.repository); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	report, buildErr := w.Build(ctx, d.repository, d.clone, BuildOptions{Adaptive: true, Backend: os.Getenv("BAUM_DATASET_BACKEND"), Model: os.Getenv("BAUM_DATASET_MODEL"),
		Progress: func(p BuildProgress) {
			reason := ""
			if p.Coverage != nil {
				reason = p.Coverage.Reason
			}
			t.Logf("%s: %s placed %d of %d, %d calls, $%.2f: %s", time.Since(started).Round(time.Second), p.Phase, p.Placed, p.Total, p.Calls, p.SpentUSD, reason)
		}})
	failure := ""
	if buildErr != nil {
		failure = buildErr.Error()
		t.Errorf("build: %v", buildErr)
	}
	out := map[string]any{"report": report, "error": failure, "seconds": time.Since(started).Seconds()}
	if state, err := d.store.adaptiveState(ctx, report.Version); err == nil && state != nil {
		catalog := map[int]string{}
		for n, p := range state.Catalog {
			catalog[n] = p.Title
		}
		out["state"] = map[string]any{"since": state.Since, "windows": state.Windows, "catalog": catalog, "details": state.Details, "selected": state.Selected,
			"scopes": state.Scopes, "assignments": state.Assignments, "feedback": state.Feedback, "step": state.Step, "source": state.Source,
			"code": len(state.Code), "decisions": state.Decisions}
	}
	d.write("build.json", out)
}

// TestDatasetLive runs the live stages a daemon pass runs after a sync:
// placement, semantic attribution and feature text.
func TestDatasetLive(t *testing.T) {
	d := openDataset(t)
	ctx := context.Background()
	w := d.worker()
	o := FeatureOptions{Manual: true, Backend: os.Getenv("BAUM_DATASET_BACKEND"), Model: os.Getenv("BAUM_DATASET_MODEL"), Limit: 5, AttributionLimit: 5}
	out := map[string]any{}
	attribution, err := w.AttributeDecisions(ctx, d.repository, d.clone, o)
	out["attribution"], out["attribution_error"] = attribution, errorText(err)
	summary, err := w.Summarize(ctx, d.repository, d.clone, o)
	out["summary"], out["summary_error"] = summary, errorText(err)
	d.write("live.json", out)
}

func errorText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
