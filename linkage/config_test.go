package linkage

import (
	"testing"
	"time"
)

func TestDefaultConfigValues(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.ExcludeMergeCommits {
		t.Error("ExcludeMergeCommits = false, want true")
	}
	if cfg.Timing.Tolerance != 24*time.Hour {
		t.Errorf("Timing.Tolerance = %s, want 24h", cfg.Timing.Tolerance)
	}
	if cfg.Timing.GapConfidenceFloor != 0.5 {
		t.Errorf("Timing.GapConfidenceFloor = %v, want 0.5", cfg.Timing.GapConfidenceFloor)
	}
	if cfg.Timing.FullConfidenceWindow != 24*time.Hour {
		t.Errorf("Timing.FullConfidenceWindow = %s, want 24h", cfg.Timing.FullConfidenceWindow)
	}
	if cfg.FilesOverlapMinScore != 0.6 {
		t.Errorf("FilesOverlapMinScore = %v, want 0.6", cfg.FilesOverlapMinScore)
	}
	if cfg.FilesOverlapMinIntersection != 2 {
		t.Errorf("FilesOverlapMinIntersection = %d, want 2", cfg.FilesOverlapMinIntersection)
	}
	if cfg.FilesOverlapFallbackScore != 0.15 {
		t.Errorf("FilesOverlapFallbackScore = %v, want 0.15", cfg.FilesOverlapFallbackScore)
	}
	if cfg.FilesOverlapFallbackEdits != 4 {
		t.Errorf("FilesOverlapFallbackEdits = %d, want 4", cfg.FilesOverlapFallbackEdits)
	}
	if cfg.FilesOverlapMinScoreMargin != 0.05 {
		t.Errorf("FilesOverlapMinScoreMargin = %v, want 0.05", cfg.FilesOverlapMinScoreMargin)
	}
	if cfg.FilesOverlapMinConfidence != 0.05 {
		t.Errorf("FilesOverlapMinConfidence = %v, want 0.05", cfg.FilesOverlapMinConfidence)
	}
	if cfg.FilesOverlapMaxConfidence != 0.75 {
		t.Errorf("FilesOverlapMaxConfidence = %v, want 0.75", cfg.FilesOverlapMaxConfidence)
	}
	if cfg.FilesOverlapMaxLinksPerCommit != 3 {
		t.Errorf("FilesOverlapMaxLinksPerCommit = %v, want 3", cfg.FilesOverlapMaxLinksPerCommit)
	}
	if cfg.CommitRefConfidence != 0.95 {
		t.Errorf("CommitRefConfidence = %v, want 0.95", cfg.CommitRefConfidence)
	}
	if cfg.TrailerConfidence != 0.9 {
		t.Errorf("TrailerConfidence = %v, want 0.9", cfg.TrailerConfidence)
	}
	if cfg.StartCommitConfidence != 0.9 {
		t.Errorf("StartCommitConfidence = %v, want 0.9", cfg.StartCommitConfidence)
	}
}

func TestConfigValidateTiming(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"negative tolerance", func(cfg *Config) { cfg.Timing.Tolerance = -time.Second }},
		{"negative gap floor", func(cfg *Config) { cfg.Timing.GapConfidenceFloor = -0.1 }},
		{"gap floor above one", func(cfg *Config) { cfg.Timing.GapConfidenceFloor = 1.1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestDefaultConfigIsIndependentPerCall(t *testing.T) {
	a := DefaultConfig()
	b := DefaultConfig()

	a.EnabledMatchSteps[0] = "mutated"
	a.AgentAliases["mutated"] = "mutated"

	if b.EnabledMatchSteps[0] == "mutated" {
		t.Error("DefaultConfig() calls share the EnabledMatchSteps backing array")
	}
	if _, ok := b.AgentAliases["mutated"]; ok {
		t.Error("DefaultConfig() calls share the AgentAliases map")
	}
}

func TestDefaultMatchStepsAreUnique(t *testing.T) {
	seen := make(map[string]bool, len(DefaultMatchSteps))
	for _, id := range DefaultMatchSteps {
		if seen[id] {
			t.Fatalf("duplicate default match step id: %q", id)
		}
		seen[id] = true
	}
	if len(DefaultMatchSteps) == 0 {
		t.Fatal("DefaultMatchSteps is empty")
	}
	if DefaultMatchSteps[len(DefaultMatchSteps)-1] != "files_overlap" {
		t.Fatal("files_overlap must run last: it is the only ranking finalizer")
	}
}

func TestDefaultAgentAliasesMapToKnownSources(t *testing.T) {
	known := map[string]bool{"claude_code": true, "codex": true, "cursor": true, "copilot": true}
	for trailer, source := range DefaultAgentAliases() {
		if !known[source] {
			t.Errorf("alias %q maps to unknown source %q", trailer, source)
		}
	}
}

// Alias keys must be the agent slugs the commit enrichment
// actually emits (it identifies agents by trailer email, never by
// display name), or coauthor_agent can never hit them.
func TestDefaultAgentAliasKeysAreEnrichmentSlugs(t *testing.T) {
	enrichmentSlugs := map[string]bool{
		"claude": true, "cursor": true, "opencode": true, "openhands": true,
		"amp": true, "aider": true, "copilot": true, "codex": true,
		"devin": true, "jules": true, "amazon-q": true,
	}
	for key := range DefaultAgentAliases() {
		if !enrichmentSlugs[key] {
			t.Errorf("alias key %q is never emitted by commit enrichment, so it is dead", key)
		}
	}
}

// commit_ref hard-links a run that recorded creating this exact SHA. No soft
// filter can improve on that evidence, and any of them can drop the run before
// it is considered, so it has to come first.
func TestCommitRefRunsBeforeSoftFilters(t *testing.T) {
	if len(DefaultMatchSteps) == 0 || DefaultMatchSteps[0] != "commit_ref" {
		t.Fatalf("DefaultMatchSteps = %v, want commit_ref first", DefaultMatchSteps)
	}
}

// The SQL candidate prefilter loads commit_ref ∪ timeframe. That is a superset
// of what the chain can match only for the step orders below that say so, and
// the resolver drops the prefilter for the rest — an ablation that silently
// loses the matches its own chain would have made reports a wrong experiment,
// not a slow one.
func TestCandidatePrefilterSoundness(t *testing.T) {
	cases := []struct {
		what  string
		steps []string
		sound bool
	}{
		{"the default order", DefaultMatchSteps, true},
		{"timeframe removed",
			[]string{"commit_ref", "coauthor_agent", "user_email", "start_commit", "branch", "files_overlap"},
			false},
		{"files_overlap before timeframe",
			[]string{"commit_ref", "files_overlap", "timeframe", "coauthor_agent", "user_email", "start_commit", "branch"},
			false},
		{"start_commit before timeframe",
			[]string{"commit_ref", "start_commit", "timeframe", "coauthor_agent", "user_email", "branch", "files_overlap"},
			false},
		{"commit_ref after timeframe",
			[]string{"timeframe", "coauthor_agent", "commit_ref", "user_email", "start_commit", "branch", "files_overlap"},
			true},
		{"filters reordered around timeframe",
			[]string{"branch", "user_email", "commit_ref", "coauthor_agent", "timeframe", "start_commit", "files_overlap"},
			true},
		{"no step at all", nil, true},
		{"only steps the prefilter's own arms cover",
			[]string{"commit_ref", "branch"}, true},
		{"an unrecognised step before timeframe",
			[]string{"commit_ref", "bot_convention", "timeframe", "files_overlap"}, false},
		{"an unrecognised step after timeframe",
			[]string{"commit_ref", "timeframe", "bot_convention", "files_overlap"}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.what, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.EnabledMatchSteps = testCase.steps
			if got := cfg.CandidatePrefilterSound(); got != testCase.sound {
				t.Fatalf("CandidatePrefilterSound() = %v with %v, want %v",
					got, testCase.steps, testCase.sound)
			}
		})
	}
}
