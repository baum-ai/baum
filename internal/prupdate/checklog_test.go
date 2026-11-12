package prupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func logChecks() []string {
	return []string{
		`{"id":21,"name":"test","status":"completed","conclusion":"failure","app":{"slug":"github-actions"},"output":{}}`,
		`{"id":22,"name":"build","status":"in_progress","conclusion":null,"app":{"slug":"github-actions"},"output":{}}`,
		`{"id":25,"name":"ci/circle","status":"completed","conclusion":"failure","app":{"slug":"circleci"},"output":{}}`,
	}
}

func logFetches(fake *fakeGitHub) int {
	n := 0
	for _, path := range fake.gets {
		if strings.HasSuffix(path, "/logs") {
			n++
		}
	}
	return n
}

func TestCheckLogKeepsTheLastLines(t *testing.T) {
	var log strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&log, "2026-11-01T10:00:%02d.1234567Z line %d\r\n", i%60, i)
	}
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: log.String()}}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 21)
	if err != nil || !got.Available || got.Name != "test" || got.ID != 21 {
		t.Fatalf("log %+v, %v", got, err)
	}
	lines := strings.Split(got.Log, "\n")
	if len(lines) != MaxLogLines || lines[0] != "line 801" || lines[len(lines)-1] != "line 1000" || !got.Truncated {
		t.Fatalf("tail of %d lines, first %q last %q, truncated %v", len(lines), lines[0], lines[len(lines)-1], got.Truncated)
	}
}

func TestCheckLogKeepsAtMostTheByteBound(t *testing.T) {
	var log strings.Builder
	for i := 1; i <= 150; i++ {
		fmt.Fprintf(&log, "%03d %s\n", i, strings.Repeat("x", 1000))
	}
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: log.String()}}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 21)
	if err != nil || !got.Available {
		t.Fatalf("log %+v, %v", got, err)
	}
	lines := strings.Split(got.Log, "\n")
	if len(got.Log) > MaxLogBytes || len(got.Log) < MaxLogBytes-2000 || !strings.HasPrefix(lines[len(lines)-1], "150 ") || !got.Truncated {
		t.Fatalf("%d bytes in %d lines, last %.10q, truncated %v", len(got.Log), len(lines), lines[len(lines)-1], got.Truncated)
	}
	// Only whole lines are kept.
	for _, line := range lines {
		if len(line) != 1004 {
			t.Fatalf("a line was cut: %d bytes", len(line))
		}
	}
}

func TestCheckLogCutsALongLine(t *testing.T) {
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: strings.Repeat("y", 10000) + "\nafter\n"}}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 21)
	lines := strings.Split(got.Log, "\n")
	if err != nil || len(lines) != 2 || len(lines[0]) > maxLogLine+len("…") || !strings.HasSuffix(lines[0], "…") || lines[1] != "after" || !got.Truncated {
		t.Fatalf("log %d lines %+v, %v", len(lines), got, err)
	}
}

func TestCheckLogDropsEscapesTimestampsAndControlCharacters(t *testing.T) {
	raw := "\xef\xbb\xbf2026-11-01T10:00:00.0000000Z \x1b[31;1mError:\x1b[0m bad\x07 thing\x00\n" +
		"2026-11-01T10:00:01Z\tsecond\x1b[2K\x1b]0;title\x07 line\r\n" +
		"\x1b[36;1mgo test\x1b[0m\n"
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: raw}}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 21)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got.Log {
		if r < ' ' && r != '\n' || r == 0x7f {
			t.Fatalf("control character %q in %q", r, got.Log)
		}
	}
	want := "Error: bad thing\n2026-11-01T10:00:01Z second]0;title line\ngo test"
	if got.Log != want {
		t.Fatalf("log %q, want %q", got.Log, want)
	}
	if got.Truncated {
		t.Fatal("a short log is not truncated")
	}
}

func TestCheckLogRedactsSecrets(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a1B2", 9)
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: "token " + secret + "\nok\n"}}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 21)
	if err != nil || strings.Contains(got.Log, secret) || !strings.Contains(got.Log, "ok") {
		t.Fatalf("log %q, %v", got.Log, err)
	}
}

func TestCheckLogOfAnotherAppHasNone(t *testing.T) {
	fake := fakeGitHub{head: "abc", checks: logChecks()}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 25)
	if err != nil || got.Available || got.Reason != LogNone || got.Message == "" || got.Log != "" || logFetches(&fake) != 0 {
		t.Fatalf("log %+v, %v, %d log reads", got, err, logFetches(&fake))
	}
}

func TestCheckLogOfARunningCheckHasNoneYet(t *testing.T) {
	fake := fakeGitHub{head: "abc", checks: logChecks()}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 22)
	if err != nil || got.Available || got.Reason != LogRunning || logFetches(&fake) != 0 {
		t.Fatalf("log %+v, %v, %d log reads", got, err, logFetches(&fake))
	}
}

func TestCheckLogRefusesACheckNotOnThePullRequest(t *testing.T) {
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{99: "other"}}
	s := Service{Client: fake.client(), Cache: &Cache{}}
	for _, check := range []int64{99, 0, -1} {
		_, err := s.CheckLog(context.Background(), repo, 7, check)
		code(t, err, NotFound)
	}
	if logFetches(&fake) != 0 {
		t.Fatalf("a log was read for a check that is not the pull request's: %v", fake.gets)
	}
}

func TestCheckLogIsKeptPerCheckRun(t *testing.T) {
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: "boom\n"}}
	s := Service{Client: fake.client(), Cache: &Cache{}}
	ctx := context.Background()
	for range 2 {
		got, err := s.CheckLog(ctx, repo, 7, 21)
		if err != nil || got.Log != "boom" {
			t.Fatalf("log %+v, %v", got, err)
		}
	}
	if n := logFetches(&fake); n != 1 {
		t.Fatalf("%d fetches for two reads of one log", n)
	}
	// An account change empties the cache.
	s.Cache.ResetIdentity()
	if _, err := s.CheckLog(ctx, repo, 7, 21); err != nil || logFetches(&fake) != 2 {
		t.Fatalf("log kept across accounts: %d fetches, %v", logFetches(&fake), err)
	}
}

func TestLogCacheIsBounded(t *testing.T) {
	c := &Cache{}
	for i := range int64(logCacheSize + 10) {
		c.storeLog(logKey(repo, i), 0, CheckLog{ID: i})
		// Keep the first one in use.
		c.cachedLog(logKey(repo, 0))
	}
	if len(c.logs) != logCacheSize || len(c.logOrder) != logCacheSize {
		t.Fatalf("%d logs kept, want %d", len(c.logs), logCacheSize)
	}
	if _, ok := c.cachedLog(logKey(repo, 0)); !ok {
		t.Fatal("the log in use was dropped")
	}
	if _, ok := c.cachedLog(logKey(repo, 1)); ok {
		t.Fatal("the least recently used log was kept")
	}
}

func TestCheckLogUnavailable(t *testing.T) {
	for _, status := range []int{404, 410} {
		fake := fakeGitHub{head: "abc", checks: logChecks(), logStatus: map[int]int{21: status}}
		s := Service{Client: fake.client(), Cache: &Cache{}}
		got, err := s.CheckLog(context.Background(), repo, 7, 21)
		if err != nil || got.Available || got.Reason != LogUnavailable || !strings.Contains(got.Message, "may not have run") {
			t.Fatalf("status %d: log %+v, %v", status, got, err)
		}
		// It is not kept: GitHub may still be writing the log.
		if _, err := s.CheckLog(context.Background(), repo, 7, 21); err != nil || logFetches(&fake) != 2 {
			t.Fatalf("status %d: %d fetches for two reads, %v", status, logFetches(&fake), err)
		}
	}
}

// Only a run whose job ran has a log to offer.
func TestOnlyARunThatRanHasALog(t *testing.T) {
	var checks []string
	conclusions := []string{"failure", "success", "cancelled", "timed_out", "neutral", "skipped", "startup_failure", "action_required", "stale"}
	for i, c := range conclusions {
		checks = append(checks, fmt.Sprintf(`{"id":%d,"name":"c%d","status":"completed","conclusion":%q,"app":{"slug":"github-actions"},"output":{}}`, 100+i, i, c))
	}
	fake := fakeGitHub{head: "abc", checks: checks}
	list, err := Service{Client: fake.client()}.AllChecks(context.Background(), repo, 7, "abc")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list.Checks {
		want := slicesContains([]string{"failure", "success", "cancelled", "timed_out", "neutral"}, c.Conclusion)
		if c.HasLog != want {
			t.Errorf("%s: has_log %v", c.Conclusion, c.HasLog)
		}
	}
}

func slicesContains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// A log far over the bound is read from the end by a range request on the
// download host.
func TestCheckLogOfALongLogIsReadFromItsEnd(t *testing.T) {
	var log strings.Builder
	for i := 1; i <= 300000; i++ {
		fmt.Fprintf(&log, "2026-11-01T10:00:00.0000000Z line %d padding padding padding\n", i)
	}
	whole := log.String() // over 16 MiB
	var ranges []string
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: ""}, logHost: func(r *http.Request) *http.Response {
		ranges = append(ranges, r.Header.Get("Range"))
		var n int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=-%d", &n); err != nil {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(whole))}
		}
		// The first line of the part is cut in the middle.
		return &http.Response{StatusCode: 206, Body: io.NopCloser(strings.NewReader(whole[len(whole)-n:]))}
	}}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 21)
	if err != nil || !got.Available || !got.Truncated || len(ranges) != 1 || ranges[0] != fmt.Sprintf("bytes=-%d", maxTailRead) {
		t.Fatalf("log %+v, %v, ranges %v", got, err, ranges)
	}
	lines := strings.Split(got.Log, "\n")
	if len(lines) != MaxLogLines || lines[len(lines)-1] != "line 300000 padding padding padding" {
		t.Fatalf("%d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

// A host that ignores the range, for a log over the read bound, gets a
// plain answer that the log is too long: never the wrong part of it.
func TestCheckLogTooLongToShowTheEndOf(t *testing.T) {
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: ""}, logHost: func(*http.Request) *http.Response {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(io.LimitReader(zeroLines{}, 65<<20))}
	}}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 21)
	if err != nil || got.Available || got.Reason != LogTooLong || got.Log != "" || got.Message == "" {
		t.Fatalf("log %+v, %v", got, err)
	}
}

type zeroLines struct{}

func (zeroLines) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
		if i%80 == 79 {
			p[i] = '\n'
		}
	}
	return len(p), nil
}

func TestCheckLogDropsInvisibleFormatCharacters(t *testing.T) {
	raw := "a‮b​c⁦d‎️e f\n"
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: raw}}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 21)
	if err != nil || got.Log != "abcdef" {
		t.Fatalf("log %q, %v", got.Log, err)
	}
}

// A secret that straddles the cut of a long line is masked, not left as a
// half token.
func TestCheckLogRedactsASecretAcrossTheCut(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a1B2", 9)
	line := strings.Repeat("x", maxLogLine-10) + " " + secret + " end"
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: line + "\n"}}
	got, err := Service{Client: fake.client()}.CheckLog(context.Background(), repo, 7, 21)
	if err != nil || !got.Available {
		t.Fatalf("log %+v, %v", got, err)
	}
	if strings.Contains(got.Log, "ghp_") || strings.Contains(got.Log, "a1B2") {
		t.Fatalf("a part of the secret is shown: %q", got.Log[len(got.Log)-40:])
	}
	if len(got.Log) > maxLogLine+len("…") {
		t.Fatalf("line of %d bytes", len(got.Log))
	}
}

func TestCheckLogReadsOnceForConcurrentOpens(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	started := 0
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: ""}, logHost: func(*http.Request) *http.Response {
		mu.Lock()
		started++
		mu.Unlock()
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("boom\n"))}
	}}
	s := Service{Client: fake.client(), Cache: &Cache{}}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := s.CheckLog(context.Background(), repo, 7, 21); err != nil || got.Log != "boom" {
				t.Errorf("log %+v, %v", got, err)
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()
	if started != 1 {
		t.Fatalf("%d downloads for concurrent opens", started)
	}
}

func TestCheckLogGitHubRefusals(t *testing.T) {
	for status, wantCode := range map[int]string{401: Forbidden, 403: Failed, 429: Failed, 500: Failed} {
		fake := fakeGitHub{head: "abc", checks: logChecks(), logStatus: map[int]int{21: status}}
		_, err := Service{Client: fake.client(), Cache: &Cache{}}.CheckLog(context.Background(), repo, 7, 21)
		code(t, err, wantCode)
	}
}

// Nothing reads a log until a person opens one: the reads of the checks do
// not.
func TestChecksAreReadWithoutTheirLogs(t *testing.T) {
	fake := fakeGitHub{head: "abc", checks: logChecks(), logs: map[int]string{21: "boom\n"}}
	s := Service{Client: fake.client(), Cache: &Cache{}}
	ctx := context.Background()
	if _, err := s.AllChecks(ctx, repo, 7, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailingChecks(ctx, repo, 7, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RerunOptions(ctx, repo, 7); err != nil {
		t.Fatal(err)
	}
	if n := logFetches(&fake); n != 0 {
		t.Fatalf("%d logs read in the background", n)
	}
}
