package traceupload

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/goredact"

	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

// fakeGitHubToken has the shape of a GitHub token, so redaction detects it;
// it is split so secret scanners do not take the source for a leak.
const fakeGitHubToken = "ghp_" + "JTeKdNnFRIBXuDL7DxtpYlSXpfKtHF4vUCsM"

func TestBuildRedactsSessionsAndWritesManifest(t *testing.T) {
	// The planted token has to look real: goredact's validators reject
	// placeholder bodies (repeated characters, alphabet runs) by design, so a
	// fixture secret must carry realistic entropy to be detected at all.
	secret := fakeGitHubToken
	sourcePath := filepath.Join(t.TempDir(), "private", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"event","token":"` + secret + `","message":"keep me"}` + "\n\n"
	if err := os.WriteFile(sourcePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	bundle, err := BuildWithProgress(context.Background(), []collector.Session{{
		Source: vendors.Codex, Path: sourcePath, SessionID: "session/id", ModifiedAt: time.Now().UTC(),
	}}, Repository{
		ID: "repo-id", FullName: "acme/demo",
		RootInventory: []RepositoryRoots{
			{ID: "repo-z", FullName: "acme/zeta", Roots: []string{"/z/worktree", "/z/checkout"}},
			{ID: "repo-id", FullName: "acme/demo", Roots: []string{"/demo"}},
		},
	}, "test", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	archivePath := bundle.File.Name()
	defer func() { _ = bundle.Close() }()
	// The CLI compares UnpackedSize with the bound the worker applies to the
	// decompressed stream, so it has to be that stream's length.
	if got := unpackedLength(t, archivePath); bundle.UnpackedSize != got {
		t.Errorf("UnpackedSize = %d, want the %d bytes the archive unpacks to", bundle.UnpackedSize, got)
	}

	files := readBundle(t, bundle.File)
	manifestData := files["manifest.json"]
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest.SchemaVersion != ManifestVersion || manifest.Repository.ID != "repo-id" || manifest.CLIVersion != "test" {
		t.Errorf("manifest metadata = %+v", manifest)
	}
	if len(manifest.RepositoryRoots) != 2 || manifest.RepositoryRoots[0].ID != "repo-id" ||
		manifest.RepositoryRoots[1].Roots[0] != "/z/checkout" {
		t.Fatalf("repository root inventory = %+v", manifest.RepositoryRoots)
	}
	if len(manifest.Sessions) != 1 || len(manifest.Sessions[0].Files) != 1 {
		t.Fatalf("manifest sessions = %+v", manifest.Sessions)
	}
	// A session with no discovered tree still names its transcript, and the
	// member says nothing about where the file lived.
	file := manifest.Sessions[0].Files[0]
	if file.Member != "sessions/000001/files/000001" || file.Name != "session.jsonl" || !file.Root {
		t.Fatalf("manifest file = %+v", file)
	}
	redacted := string(files[file.Member])
	if strings.Contains(redacted, secret) {
		t.Fatal("redacted session contains original secret")
	}
	if !strings.Contains(redacted, "keep me") || !strings.HasSuffix(redacted, "\n\n") {
		t.Errorf("redacted session lost ordinary content: %q", redacted)
	}
	if len(manifest.Redaction.FindingsByType) == 0 {
		t.Errorf("redaction findings are empty: %+v", manifest.Redaction)
	}
	if manifest.Redaction.Tool != "goredact" || manifest.Redaction.Profile != "balanced" ||
		manifest.Redaction.Strategy != "format-preserving" || manifest.Redaction.RuleSet == "" {
		t.Errorf("redaction summary does not pin the detector: %+v", manifest.Redaction)
	}
	if strings.Contains(string(manifestData), sourcePath) || strings.Contains(string(manifestData), secret) {
		t.Fatal("manifest contains a source path or matched secret")
	}

	if err := bundle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Errorf("temporary archive still exists after Close: %v", err)
	}
}

func unpackedLength(t *testing.T, archivePath string) int64 {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	n, err := io.Copy(io.Discard, gz)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBuildRedactsGeneratedSessionBytes(t *testing.T) {
	secret := fakeGitHubToken
	body := []byte(`{"schema_version":"1","tree":{"prompt":"` + secret + `"}}` + "\n")
	bundle, err := BuildWithProgress(context.Background(), []collector.Session{{
		Source: vendors.Cursor, SessionID: "cursor-1", ModifiedAt: time.Now().UTC(),
		Files: []collector.SessionFile{{Data: body, Name: "cursor-tree.json", Kind: model.KindTranscript}},
	}}, Repository{}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bundle.Close() }()

	files := readBundle(t, bundle.File)
	entry := bundle.Manifest.Sessions[0].Files[0]
	redacted := files[entry.Member]
	if bytes.Contains(redacted, []byte(secret)) || !json.Valid(redacted) {
		t.Fatalf("generated bytes were not safely redacted: %s", redacted)
	}
}

// TestBuildRedactsAndIndexesASessionTree: every file of a tree is redacted and
// listed, under an opaque member with the name and kind its source gave it.
func TestBuildRedactsAndIndexesASessionTree(t *testing.T) {
	const (
		sessionID = "11111111-2222-4333-8444-555555555555"
		secret    = fakeGitHubToken
		toolUseID = "toolu_01SRxYT9mEzczk8zBssgriq7"
		agentID   = "a656514b90a0ed991"
	)
	rootDir := t.TempDir()
	rootPath := filepath.Join(rootDir, sessionID+".jsonl")
	subagentPath := filepath.Join(rootDir, sessionID, "subagents", "agent-a.jsonl")
	metaPath := filepath.Join(rootDir, sessionID, "subagents", "agent-a.meta.json")
	for path, content := range map[string]string{
		rootPath:     `{"type":"user","sessionId":"` + sessionID + `"}` + "\n",
		subagentPath: `{"agentId":"` + agentID + `","tool_use_id":"` + toolUseID + `","token":"` + secret + `"}` + "\n",
		metaPath:     "{\n  \"parentAgentId\": \"" + agentID + "\",\n  \"toolUseId\": \"" + toolUseID + "\",\n  \"token\": \"" + secret + "\"\n}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want := []collector.SessionFile{
		{Path: rootPath, Name: sessionID + ".jsonl", Kind: model.KindTranscript},
		{Path: subagentPath, Name: "agent-a.jsonl", Kind: model.KindSubTranscript},
		{Path: metaPath, Name: "agent-a.meta.json", Kind: model.KindSubMetadata},
	}
	bundle, err := BuildWithProgress(context.Background(), []collector.Session{{
		Source: vendors.ClaudeCode, Path: rootPath, SessionID: sessionID,
		ModifiedAt: time.Now().UTC(), Files: want,
	}}, Repository{ID: "repo-id", FullName: "acme/demo"}, "test", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = bundle.Close() }()

	files := readBundle(t, bundle.File)
	entry := bundle.Manifest.Sessions[0]
	if bundle.Manifest.SchemaVersion != ManifestVersion {
		t.Fatalf("schema version = %q, want %q", bundle.Manifest.SchemaVersion, ManifestVersion)
	}
	if len(entry.Files) != len(want) {
		t.Fatalf("manifest files = %+v", entry.Files)
	}
	findings := 0
	for i, file := range entry.Files {
		if file.Name != want[i].Name || file.Kind != want[i].Kind {
			t.Errorf("files[%d] = (%q, %q), want (%q, %q)", i, file.Name, file.Kind, want[i].Name, want[i].Kind)
		}
		if file.Member != fmt.Sprintf("sessions/000001/files/%06d", i+1) {
			t.Errorf("files[%d].member = %q", i, file.Member)
		}
		if file.Root != (i == 0) {
			t.Errorf("files[%d].root = %v", i, file.Root)
		}
		body := string(files[file.Member])
		if strings.Contains(body, secret) {
			t.Errorf("%s contains planted secret", file.Name)
		}
		if i > 0 && !strings.Contains(body, toolUseID) {
			t.Errorf("%s lost structural id", file.Name)
		}
		if i > 0 && !strings.Contains(body, agentID) {
			t.Errorf("%s lost subagent id", file.Name)
		}
		for _, count := range file.FindingsByType {
			findings += count
		}
	}
	// No host layout leaves the machine: the manifest and the member names carry
	// only names the source chose and indices the builder chose.
	manifestJSON := string(files["manifest.json"])
	for _, leaked := range []string{rootDir, "subagents"} {
		if strings.Contains(manifestJSON, leaked) {
			t.Errorf("manifest leaks the local layout %q", leaked)
		}
	}
	if got := sumCounts(entry.FindingsByType); got != findings || got == 0 {
		t.Errorf("session findings = %d, per-file total = %d", got, findings)
	}
}

func TestBuildRejectsMalformedFileSets(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(sourcePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, files := range map[string][]collector.SessionFile{
		"duplicate name": {
			{Path: sourcePath, Name: "session.jsonl", Kind: model.KindTranscript},
			{Path: sourcePath, Name: "session.jsonl", Kind: model.KindSubTranscript},
		},
		"unnamed file": {
			{Path: sourcePath, Kind: model.KindTranscript},
		},
		"no root": {
			{Path: sourcePath, Name: "session.jsonl", Kind: model.KindSubTranscript},
		},
		"two roots": {
			{Path: sourcePath, Name: "a.jsonl", Kind: model.KindTranscript},
			{Path: sourcePath, Name: "b.jsonl", Kind: model.KindTranscript},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildWithProgress(context.Background(), []collector.Session{{
				Source: vendors.Codex, Path: sourcePath, SessionID: "id", Files: files,
			}}, Repository{}, "test", nil)
			if err == nil {
				t.Fatal("Build succeeded")
			}
		})
	}
}

func sumCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func TestBuildHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := BuildWithProgress(ctx, []collector.Session{{Source: vendors.Codex, Path: path, SessionID: "id"}}, Repository{}, "test", nil)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("Build error = %v, want context cancellation", err)
	}
}

func TestBuildPreservesSessionOrderWhenRedactingConcurrently(t *testing.T) {
	const sessionCount = 16
	dir := t.TempDir()
	secret := fakeGitHubToken
	sessions := make([]collector.Session, 0, sessionCount)
	for i := range sessionCount {
		path := filepath.Join(dir, fmt.Sprintf("%02d.jsonl", i))
		content := fmt.Sprintf(`{"index":%d,"token":%q}`+"\n", i, secret)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, collector.Session{
			Source:     vendors.Codex,
			Path:       path,
			SessionID:  fmt.Sprintf("session-%02d", i),
			ModifiedAt: time.Now().UTC(),
		})
	}

	var progress []int
	bundle, err := BuildWithProgress(context.Background(), sessions, Repository{}, "test", func(completed, total int) {
		if total != sessionCount {
			t.Errorf("progress total = %d, want %d", total, sessionCount)
		}
		progress = append(progress, completed)
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = bundle.Close() }()
	for i, completed := range progress {
		if completed != i+1 {
			t.Errorf("progress[%d] = %d, want %d", i, completed, i+1)
		}
	}
	if len(progress) != sessionCount {
		t.Fatalf("got %d progress updates, want %d", len(progress), sessionCount)
	}
	files := readBundle(t, bundle.File)
	for i, entry := range bundle.Manifest.Sessions {
		wantID := fmt.Sprintf("session-%02d", i)
		wantMember := fmt.Sprintf("sessions/%06d/files/000001", i+1)
		if entry.SessionID != wantID || entry.Files[0].Member != wantMember {
			t.Errorf("session %d = (%q, %q), want (%q, %q)", i, entry.SessionID, entry.Files[0].Member, wantID, wantMember)
		}
		if strings.Contains(string(files[wantMember]), secret) {
			t.Errorf("session %d contains original secret", i)
		}
	}
}

// Agent-generated tool/message ids are the only link between a tool call and
// its result, so masking them collapses unrelated calls onto one identifier
// and breaks ingest — the previous engine (ferret-scan) read them as
// look-alike API keys and needed an exemption pass to undo it. goredact must
// simply not detect them, while real secrets on the very same line still
// redact.
func TestBuildKeepsStructuralIDsAndStillRedactsSecrets(t *testing.T) {
	const (
		toolUseID = "toolu_01SRxYT9mEzczk8zBssgriq7"
		messageID = "msg_011CdJksKGzLJR6RSUrf3JL2"
		requestID = "req_011CdJktBWwmuxF4zdYMYy9a"
		callID    = "call_1s4autQB5qXdt5TD7U96Xf5U"
		secret    = fakeGitHubToken
	)
	sourcePath := filepath.Join(t.TempDir(), "session.jsonl")
	// The secret shares a line with a structural id, so exempting the id must
	// not exempt its neighbour.
	content := `{"id":"` + messageID + `","requestId":"` + requestID + `","token":"` + secret + `"}` + "\n" +
		`{"tool_use_id":"` + toolUseID + `","call_id":"` + callID + `"}` + "\n"
	if err := os.WriteFile(sourcePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	bundle, err := BuildWithProgress(context.Background(), []collector.Session{{
		Source: vendors.ClaudeCode, Path: sourcePath, SessionID: "s1", ModifiedAt: time.Now().UTC(),
	}}, Repository{ID: "repo-id", FullName: "acme/demo"}, "test", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = bundle.Close() }()

	files := readBundle(t, bundle.File)
	redacted := string(files[bundle.Manifest.Sessions[0].Files[0].Member])

	for _, id := range []string{toolUseID, messageID, requestID, callID} {
		if !strings.Contains(redacted, id) {
			t.Errorf("structural id %q was redacted; distinct ids would collide", id)
		}
	}
	if strings.Contains(redacted, secret) {
		t.Fatal("real secret survived redaction")
	}
	if len(bundle.Manifest.Redaction.FindingsByType) == 0 {
		t.Errorf("manifest does not record the redacted findings: %+v", bundle.Manifest.Redaction)
	}
}

// Transcripts embed JSON inside JSON strings, so ids and paths are written
// `\"toolu_…\"`. The previous engine's match swallowed the escape's backslash
// and masking rewrote it — turning `\"` into `*"`, closing the enclosing
// string early and leaving a record no reader could parse. On this
// repository's own transcripts that silently cost 44 records across 12 of 77
// files, and the archive dropped 15 of 53 sessions.
//
// The record must still parse, the escaped structural ids must survive, and the
// escaped secret must still be masked without taking its delimiter with it.
func TestBuildKeepsEscapedRecordsParseable(t *testing.T) {
	const (
		toolUseID = "toolu_01R4JB487M6Q5tbmPXkDfzSG"
		cseID     = "cse_01VLMf975o3vrgtXNbiDDidP"
		filePath  = "/home/dev/Documents/work/acme/code/data_matching/LOCAL_SESSION_DISCOVERY_SPEC"
		secret    = fakeGitHubToken
	)
	// A tool_result whose content is a JSON document carried as a string: every
	// inner quote is escaped, so every inner token abuts a backslash.
	inner := `{\"tool_use_id\": \"` + toolUseID + `\", \"entry\": \"` + cseID +
		`\", \"filename\": \"` + filePath + `\", \"token\": \"` + secret + `\"}`
	content := `{"type":"tool_result","content":"attachment[file]: ` + inner + `"}` + "\n"

	sourcePath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(sourcePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	bundle, err := BuildWithProgress(context.Background(), []collector.Session{{
		Source: vendors.ClaudeCode, Path: sourcePath, SessionID: "s1", ModifiedAt: time.Now().UTC(),
	}}, Repository{ID: "repo-id", FullName: "acme/demo"}, "test", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = bundle.Close() }()

	files := readBundle(t, bundle.File)
	redacted := string(files[bundle.Manifest.Sessions[0].Files[0].Member])

	var record any
	if err := json.Unmarshal([]byte(strings.TrimSpace(redacted)), &record); err != nil {
		t.Fatalf("redaction produced a record no reader can parse: %v\n%s", err, redacted)
	}
	for _, id := range []string{toolUseID, cseID} {
		if !strings.Contains(redacted, id) {
			t.Errorf("escaped structural id %q was redacted; distinct ids would collide", id)
		}
	}
	if strings.Contains(redacted, secret) {
		t.Error("real secret survived redaction")
	}
}

// The redaction stream must be the source bytes with only detected spans
// rewritten: surrounding records, blank lines, and a secret placed megabytes
// into the file all have to come out exactly as they went in. The late secret
// crosses the engine's internal chunk boundaries, which is where a streaming
// redactor would lose or split it.
func TestRedactionPreservesRecordBytes(t *testing.T) {
	const secret = fakeGitHubToken
	filler := `{"type":"event_msg","payload":{"type":"agent_message","message":"ordinary transcript content that carries no credentials at all"}}` + "\n"
	content := "ordinary first record\n\n" +
		strings.Repeat(filler, 24_000) +
		`{"token":"` + secret + `"}` + "\n" +
		"\nordinary final record\n"
	if len(content) < 2<<20 {
		t.Fatalf("fixture is %d bytes, want at least 2 MiB to cross chunk boundaries", len(content))
	}

	engine, err := goredact.New(redactionConfig)
	if err != nil {
		t.Fatalf("goredact.New: %v", err)
	}
	var output bytes.Buffer
	stats, err := engine.Redact(context.Background(), &output, strings.NewReader(content))
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatal("output contains original secret")
	}
	if !strings.HasPrefix(output.String(), "ordinary first record\n\n") ||
		!strings.HasSuffix(output.String(), "\nordinary final record\n") {
		t.Error("record boundaries changed")
	}
	// Format-preserving masking rewrites spans in place, so nothing may
	// change length.
	if output.Len() != len(content) || stats.BytesWritten != int64(len(content)) {
		t.Errorf("output = %d bytes, stats = %d, want %d", output.Len(), stats.BytesWritten, len(content))
	}
	if len(stats.ByRule) == 0 {
		t.Error("no findings recorded")
	}
}

// A NUL byte marks binary content, which must fail the upload rather than be
// archived as if it were a session.
func TestBuildRejectsBinaryContent(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(sourcePath, []byte("{\"data\":\"a\x00b\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := BuildWithProgress(context.Background(), []collector.Session{{
		Source: vendors.Codex, Path: sourcePath, SessionID: "id", ModifiedAt: time.Now().UTC(),
	}}, Repository{}, "test", nil)
	if err == nil || !strings.Contains(err.Error(), "binary content") {
		t.Fatalf("Build error = %v, want binary-content rejection", err)
	}
}

func BenchmarkBuild81Sessions(b *testing.B) {
	const (
		sessionCount = 81
		records      = 500
	)
	dir := b.TempDir()
	sessions := make([]collector.Session, 0, sessionCount)
	record := `{"timestamp":"2026-07-23T12:00:00Z","type":"event_msg","payload":{"type":"agent_message","message":"ordinary benchmark content with enough text to exercise secret scanning realistically"}}` + "\n"
	content := strings.Repeat(record, records)
	for i := range sessionCount {
		path := filepath.Join(dir, fmt.Sprintf("%03d.jsonl", i))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			b.Fatal(err)
		}
		sessions = append(sessions, collector.Session{
			Source:     vendors.Codex,
			Path:       path,
			SessionID:  fmt.Sprintf("session-%03d", i),
			ModifiedAt: time.Now().UTC(),
		})
	}

	b.SetBytes(int64(len(content) * sessionCount))
	b.ResetTimer()
	for range b.N {
		bundle, err := BuildWithProgress(context.Background(), sessions, Repository{ID: "repo-id", FullName: "acme/demo"}, "bench", nil)
		if err != nil {
			b.Fatal(err)
		}
		if err := bundle.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildDiscoveredSessions(b *testing.B) {
	root := os.Getenv("BAUM_BENCH_ROOT")
	if root == "" {
		b.Skip("set BAUM_BENCH_ROOT to profile locally discovered sessions")
	}
	sessions, _, err := collector.FindSessions(root, collector.FindOptions{})
	if err != nil {
		b.Fatal(err)
	}
	var size int64
	for _, session := range sessions {
		files := session.Files
		if len(files) == 0 {
			files = []collector.SessionFile{{Path: session.Path}}
		}
		for _, file := range files {
			if file.Data != nil {
				size += int64(len(file.Data))
				continue
			}
			info, err := os.Stat(file.Path)
			if err != nil {
				b.Fatal(err)
			}
			size += info.Size()
		}
	}

	b.SetBytes(size)
	b.ResetTimer()
	b.ReportMetric(float64(len(sessions)), "sessions")
	for range b.N {
		bundle, err := BuildWithProgress(context.Background(), sessions, Repository{ID: "repo-id", FullName: "local/benchmark"}, "bench", nil)
		if err != nil {
			b.Fatal(err)
		}
		if err := bundle.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func readBundle(t *testing.T, file *os.File) map[string][]byte {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	files := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = data
		if header.Mode != 0o600 {
			t.Errorf("%s mode = %#o, want 0600", header.Name, header.Mode)
		}
	}
	return files
}

// The collector's roots ride the manifest, which is the only way they reach the
// platform at all. They are optional: an archive built by an older CLI copy
// carries neither and stays valid at schema_version "1".
func TestBuildCarriesRecordedRoots(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(sourcePath, []byte(`{"type":"event"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildWithProgress(context.Background(), []collector.Session{{
		Source: vendors.Codex, Path: sourcePath, SessionID: "s", ModifiedAt: time.Now().UTC(),
		RepoRoot: "/home/dev/repo", WorktreeRoot: "/home/dev/repo/.worktrees/feature",
	}}, Repository{ID: "repo-id", FullName: "acme/demo"}, "test", nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = bundle.Close() }()

	entry := bundle.Manifest.Sessions[0]
	if entry.RepoRoot != "/home/dev/repo" || entry.WorktreeRoot != "/home/dev/repo/.worktrees/feature" {
		t.Fatalf("manifest roots = %q / %q", entry.RepoRoot, entry.WorktreeRoot)
	}
}
