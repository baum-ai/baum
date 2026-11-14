// Package tilechecks is the render check of custom tile types: it checks a
// candidate type (a definition, its samples and, optionally, the data it
// would show on a pull request) without drawing it, then has the app's
// check runner draw it in sandboxed frames and report what went wrong.
//
// The daemon cannot draw: the app can, in the same engine a person's tiles
// draw in. So a check is a job in a small queue (one running, at most
// maxQueued waiting) that the runner claims with a long poll (Next) and
// answers with a report (Report). Claiming registers the candidate's frame
// document, so the frame route serves it to the runner; a claimed job that
// gets no report in time is a hang, which blocks the digest until the
// daemon restarts (tileframes.Registry.MarkHung). With no runner, a check is
// unavailable.
//
// The page's first-show preflight (Preflight) runs a pair of a definition
// and the data a tile shows through the same queue, once: verdicts are kept
// by pair in the application database.
package tilechecks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/tileframes"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
)

// The verdicts of a check.
const (
	VerdictPass        = "pass"
	VerdictFail        = "fail"
	VerdictHang        = "hang"
	VerdictUnavailable = "unavailable"
)

// The states a tile_check event names. An event carries a check's ID and
// state, never its content.
const (
	StateQueued  = "queued"
	StateRunning = "running"
	StateDone    = "done"
	StateHung    = "hung"
)

// The bounds of a check.
const (
	// MaxSamples is how many samples of its own a candidate may carry.
	MaxSamples = 3
	// maxQueued is how many checks may wait behind the one running.
	maxQueued = 4
	// maxPreflights is how many preflights may wait: a page asks for one
	// per custom tile it shows.
	maxPreflights = 16
)

// The default timings.
const (
	defaultWait    = 30 * time.Second
	defaultClaim   = 10 * time.Second
	defaultHang    = 10 * time.Second
	defaultLongest = 25 * time.Second
)

// frameBudget is what a claimed job may take per frame it draws, however
// often the runner says it is alive: Hang plus this per frame is the most a
// job may run.
var frameBudget = 3 * time.Second

var (
	// ErrBusy is a check refused because maxQueued checks already wait.
	ErrBusy = errors.New("tilechecks: other checks are waiting; try again in a few seconds")
	// ErrClosed is a check sent after Close.
	ErrClosed = errors.New("tilechecks: the render check has stopped")
	// ErrUnknown is a report for a job that is not running: it ended, or
	// never existed.
	ErrUnknown = errors.New("tilechecks: no such running check")
)

// The reasons an unavailable result gives.
const (
	reasonNoApp    = "Open the baum app to check tiles: the check draws them in the app."
	reasonNoRunner = "No window of the app took the check. Custom tiles are drawn by the baum app on macOS and Linux, or in a browser."
	reasonBusy     = "The render check was busy for too long; try again."
)

// Context names the pull request version a candidate's data is for.
type Context struct {
	Clone   string `json:"clone"`
	PR      int    `json:"pr"`
	Version int    `json:"version"`
}

// Candidate is a tile type to check: its definition (without a digest,
// which the check computes), up to MaxSamples samples of its own (data and
// the code it cites, as a library type's samples are), and optionally the
// data it would show on a pull request, with the version it is for.
type Candidate struct {
	Definition tiles.TypeDefinition `json:"definition"`
	Samples    []library.Sample     `json:"samples,omitempty"`
	Data       json.RawMessage      `json:"data,omitempty"`
	Context    *Context             `json:"context,omitempty"`
}

// Result is what a check found. The verdict is pass exactly when Errors is
// empty and the candidate was drawn; Reason says why a check was
// unavailable.
type Result struct {
	Check    string    `json:"check"`
	Digest   string    `json:"digest"`
	Kit      int       `json:"kit"`
	Verdict  string    `json:"verdict"`
	Reason   string    `json:"reason,omitempty"`
	Errors   []Problem `json:"errors"`
	Warnings []Problem `json:"warnings"`
	Runs     []Run     `json:"runs"`
	Engine   string    `json:"engine"`
}

// Report is what the runner sends for a job it drew: every problem it saw,
// unsorted (the daemon classifies them), the runs and the engine.
type Report struct {
	Problems []Problem `json:"problems"`
	Runs     []Run     `json:"runs"`
	Engine   string    `json:"engine"`
	// Abandoned says the runner stopped before it drew everything (its
	// page went away): the job ends unavailable, and its type is not
	// blocked.
	Abandoned bool `json:"abandoned,omitempty"`
}

// Event is a tile_check event's data.
type Event struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// Job is what Next hands the runner: everything needed to draw the
// candidate and nothing more. The definition is in its served form, without
// its HTML: the frame route serves that by digest.
type Job struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Digest     string          `json:"digest"`
	Kit        int             `json:"kit"`
	Title      string          `json:"title"`
	Sizes      []tiles.Size    `json:"sizes"`
	Definition json.RawMessage `json:"definition,omitempty"`
	Context    *Context        `json:"context,omitempty"`
	Samples    []JobSample     `json:"samples"`
}

// The kinds of job.
const (
	JobCheck     = "check"
	JobPreflight = "preflight"
)

// JobSample is one sample to draw. Refs are the payloads of its citations
// for each face, resolved from the samples' own hunks and items; the data
// of a check with a Context has none, since the runner resolves it against
// that version's tile document, as the page does.
type JobSample struct {
	Name string          `json:"name"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
	Refs *Refs           `json:"refs,omitempty"`
}

// Refs are a sample's citation payloads for each face.
type Refs struct {
	Glance map[string]any `json:"glance"`
	Detail map[string]any `json:"detail"`
}

// Options configure a Checker.
type Options struct {
	// Frames is the frame registry a claimed job's definition is
	// registered in; nil is tileview's.
	Frames *tileframes.Registry
	// Publish sends a tile_check event; nil sends none.
	Publish func(Event)
	// Subscribed reports whether the app's event stream is connected; nil
	// counts it as connected. With no app and no runner polling, a check
	// is unavailable at once.
	Subscribed func() bool
	// Database is the application database the verdicts of preflights and
	// checks with data are kept in; empty keeps none.
	Database string
	// Wait bounds how long Submit and Preflight wait (default 30 s); Claim,
	// how long a queued job may wait for a runner that does not poll
	// (default 10 s); Hang, how long a claimed job may go without a report
	// (default 10 s).
	Wait, Claim, Hang time.Duration
	// Log receives what the check logs; nil is slog.Default.
	Log *slog.Logger
}

// Checker runs render checks. New makes one; it is safe for concurrent use.
type Checker struct {
	opts      Options
	frames    *tileframes.Registry
	validator *tiles.Validator
	store     *store

	mu      sync.Mutex
	queue   []*job
	running *job
	// polled is when a runner last polled or reported; freed, when the
	// last running job ended.
	polled, freed time.Time
	// wake is closed, and replaced, when a job may be claimed.
	wake   chan struct{}
	closed bool
	// preflights are the queued or running preflights by pair, so a page
	// asking twice waits on one.
	preflights map[string]*job
}

type job struct {
	Job
	encoded []byte
	def     tiles.TypeDefinition
	// kinds maps each sample's name to its kind.
	kinds map[string]string
	// found are the problems the daemon found before queueing.
	errors, warnings []Problem
	// record names the pair whose verdict is kept, when there is one.
	dataSHA string
	queued  time.Time
	claimed bool
	timer   *time.Timer
	// alive is when the runner last showed it was still drawing the job;
	// limit is when the job ends as a hang however alive it is.
	alive, limit time.Time
	done         chan struct{}
	result       Result
}

// New returns a Checker. It opens the verdict store when opts.Database is
// set.
func New(opts Options) (*Checker, error) {
	if opts.Wait <= 0 {
		opts.Wait = defaultWait
	}
	if opts.Claim <= 0 {
		opts.Claim = defaultClaim
	}
	if opts.Hang <= 0 {
		opts.Hang = defaultHang
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	c := &Checker{opts: opts, frames: opts.Frames, validator: tiles.NewValidator(), wake: make(chan struct{}), preflights: map[string]*job{}}
	if c.frames == nil {
		c.frames = tileview.FrameRegistry()
	}
	if opts.Database != "" {
		s, err := openStore(opts.Database)
		if err != nil {
			return nil, err
		}
		c.store = s
	}
	return c, nil
}

// Close ends every waiting check as unavailable and closes the store.
func (c *Checker) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	pending := append(slices.Clone(c.queue), c.running)
	c.queue, c.running = nil, nil
	c.poke()
	c.mu.Unlock()
	for _, j := range pending {
		if j != nil {
			c.finish(j, unavailable(j, "The render check stopped."))
		}
	}
	if c.store != nil {
		return c.store.close()
	}
	return nil
}

// Submit checks a candidate and waits for its result, up to Options.Wait.
// The definition, its HTML (kit.Lint), the samples and the data are checked
// first: a problem there fails the check at once, with nothing drawn (lint
// warnings and citations that do not resolve are reported and the check
// goes on). Then the schema's edge samples (minimal and maximal) are made
// and the job is queued for the runner. It fails only when too many checks
// wait (ErrBusy), when ctx ends and after Close.
func (c *Checker) Submit(ctx context.Context, candidate Candidate) (Result, error) {
	j, done := c.prepare(candidate)
	if done {
		return j.result, nil
	}
	if err := c.enqueue(j, maxQueued); err != nil {
		return Result{}, err
	}
	return c.await(ctx, j)
}

// prepare runs the checks that need no drawing and builds the job. It
// reports done with the job's result set when the check ends there.
func (c *Checker) prepare(candidate Candidate) (*job, bool) {
	j := &job{Job: Job{Kind: JobCheck}, kinds: map[string]string{}, done: make(chan struct{})}
	j.ID = newID()
	def := candidate.Definition
	def.Digest = ""
	j.Kit, j.Title, j.Sizes = def.Kit, def.Title, def.Sizes
	fail := func(problems ...Problem) (*job, bool) {
		j.errors = append(j.errors, problems...)
		j.result = c.result(j, VerdictFail)
		return j, true
	}
	encoded, err := json.Marshal(def)
	if err == nil {
		def.Digest, err = tiles.DefinitionDigest(encoded)
	}
	if err == nil {
		encoded, err = json.Marshal(def)
	}
	if err != nil {
		return fail(problem(CodeDefinitionInvalid, "the definition is not valid: "+strings.TrimPrefix(err.Error(), "tiles: ")))
	}
	j.Digest = def.Digest
	if _, problems := c.validator.CheckDefinition(encoded); len(problems) > 0 {
		var found []Problem
		for _, p := range problems {
			code := CodeDefinitionInvalid
			if strings.HasPrefix(p, "data schema ") {
				code = CodeDataSchemaInvalid
			}
			found = append(found, problem(code, p))
		}
		return fail(found...)
	}
	j.def, j.encoded = def, encoded
	j.Definition = tileview.ServedDefinition(encoded)
	// A type that already hung is refused by the frame route until the
	// daemon restarts: drawing it again could only fail to start.
	if c.frames.Hung(def.Digest) {
		j.errors = append(j.errors, problem(CodeHang, "This type stopped responding when it was checked before; it stays blocked until baum restarts. Change the code."))
		j.result = c.result(j, VerdictHang)
		return j, true
	}
	var lintErrors []Problem
	for _, f := range kit.Lint(def.Kit, def.Source.HTML) {
		p := problem(CodeLint, fmt.Sprintf("tile.html:%d:%d: %s (%s)", f.Line, f.Column, f.Message, f.Rule))
		line, column := f.Line, f.Column
		p.Line, p.Column = &line, &column
		if f.Severity == kit.Error {
			lintErrors = append(lintErrors, p)
		} else {
			j.warnings = append(j.warnings, p)
		}
	}
	if len(lintErrors) > 0 {
		return fail(lintErrors...)
	}
	if len(candidate.Samples) > MaxSamples {
		return fail(problem(CodeSampleInvalid, fmt.Sprintf("a check takes at most %d samples of its own; it was given %d", MaxSamples, len(candidate.Samples))))
	}
	var invalid []Problem
	hunks, items := map[string]library.Hunk{}, map[string]library.Item{}
	var pool []json.RawMessage
	for i, s := range candidate.Samples {
		name := s.Name
		if name == "" || j.kinds[name] != "" || name == KindData || name == KindMinimal || name == KindMaximal {
			name = fmt.Sprintf("sample_%d", i+1)
		}
		j.kinds[name] = KindSample
		for _, p := range c.validator.CheckData(encoded, s.Data) {
			q := problem(CodeSampleInvalid, p)
			q.Sample = name
			invalid = append(invalid, q)
		}
		glance, unresolved := s.Refs("glance")
		detail, _ := s.Refs("detail")
		for _, at := range unresolved {
			q := problem(CodeUnresolvedCitation, fmt.Sprintf("the citation at %s does not resolve to the sample's hunks or items", at))
			q.Sample = name
			j.errors = append(j.errors, q)
		}
		for _, id := range slices.Sorted(maps.Keys(s.Hunks)) {
			hunks[id] = s.Hunks[id]
			citation, _ := json.Marshal(map[string]string{"type": "hunk", "id": id})
			pool = append(pool, citation)
		}
		maps.Copy(items, s.Items)
		j.Samples = append(j.Samples, JobSample{Name: name, Kind: KindSample, Data: s.Data, Refs: &Refs{Glance: glance, Detail: detail}})
	}
	if len(candidate.Data) > 0 {
		j.kinds[KindData] = KindData
		data := JobSample{Name: KindData, Kind: KindData, Data: candidate.Data}
		for _, p := range c.validator.CheckData(encoded, candidate.Data) {
			q := problem(CodeDataInvalid, p)
			q.Sample = KindData
			invalid = append(invalid, q)
		}
		if candidate.Context == nil {
			q := problem(CodeDataInvalid, "the data's citations resolve against a pull request version: give the version it is for")
			q.Sample = KindData
			invalid = append(invalid, q)
		}
		j.Context = candidate.Context
		j.dataSHA, _ = DataSHA(candidate.Data)
		// The data is drawn first, as it is what the tile will show.
		j.Samples = append([]JobSample{data}, j.Samples...)
	}
	if len(invalid) > 0 {
		return fail(invalid...)
	}
	edges, warnings, err := tiles.EdgeSamples(def.DataSchema, pool)
	if err != nil {
		return fail(problem(CodeDataSchemaInvalid, strings.TrimPrefix(err.Error(), "tiles: ")))
	}
	for _, w := range warnings {
		j.warnings = append(j.warnings, problem(CodeSampleInvalid, w))
	}
	for _, edge := range edges {
		merged := library.Sample{Name: edge.Name, Data: edge.Data, Hunks: hunks, Items: items}
		glance, _ := merged.Refs("glance")
		detail, _ := merged.Refs("detail")
		j.kinds[edge.Name] = edge.Name
		j.Samples = append(j.Samples, JobSample{Name: edge.Name, Kind: edge.Name, Data: edge.Data, Refs: &Refs{Glance: glance, Detail: detail}})
	}
	return j, false
}

// PreflightRequest is a tile the page is about to show for the first time:
// its definition's digest, the data it shows, the size its glance is
// placed at and its citations' payloads for each face, as the page
// resolved them.
type PreflightRequest struct {
	Digest string          `json:"digest"`
	Data   json.RawMessage `json:"data"`
	Size   tiles.Size      `json:"size"`
	Refs   Refs            `json:"refs"`
}

// PreflightResult is a preflight's verdict, with Recorded set when it was
// already kept and nothing was drawn.
type PreflightResult struct {
	Result
	Recorded bool `json:"recorded,omitempty"`
}

// Preflight draws a tile's data with its type once, as a light glance at
// its size and a detail, unless the pair's verdict is already kept. A
// digest marked hung answers hang at once; one the frame registry does not
// hold (the page's tile route registers it before it is shown) answers
// unavailable, and the page shows the tile.
func (c *Checker) Preflight(ctx context.Context, req PreflightRequest) (PreflightResult, error) {
	dataSHA, err := DataSHA(req.Data)
	if err != nil {
		return PreflightResult{}, fmt.Errorf("tilechecks: the data is not JSON: %w", err)
	}
	size := req.Size
	if size != tiles.SizeFull && size != tiles.SizeHalf && size != tiles.SizeThird {
		size = tiles.SizeFull
	}
	base := Result{Digest: req.Digest, Errors: []Problem{}, Warnings: []Problem{}, Runs: []Run{}}
	if c.frames.Hung(req.Digest) {
		base.Verdict = VerdictHang
		base.Errors = []Problem{problem(CodeHang, "This tile stopped responding when it was checked.")}
		return PreflightResult{Result: base}, nil
	}
	if c.store != nil {
		verdict, ok, err := c.store.lookup(ctx, req.Digest, dataSHA)
		if err != nil {
			c.opts.Log.Warn("a tile check verdict could not be read", "error", err.Error())
		}
		// A hang kept before the daemon restarted is drawn again: the
		// block it set lasts until a restart.
		if ok && verdict != VerdictHang {
			base.Verdict = verdict
			return PreflightResult{Result: base, Recorded: true}, nil
		}
	}
	if _, ok := c.frames.Get(req.Digest); !ok {
		base.Verdict, base.Reason = VerdictUnavailable, "The tile's type is not registered."
		return PreflightResult{Result: base}, nil
	}
	key := req.Digest + " " + dataSHA
	c.mu.Lock()
	j := c.preflights[key]
	c.mu.Unlock()
	if j == nil {
		j = &job{Job: Job{ID: newID(), Kind: JobPreflight, Digest: req.Digest, Sizes: []tiles.Size{size}}, kinds: map[string]string{KindData: KindData}, dataSHA: dataSHA, done: make(chan struct{})}
		refs := req.Refs
		j.Samples = []JobSample{{Name: KindData, Kind: KindData, Data: req.Data, Refs: &refs}}
		c.mu.Lock()
		if existing := c.preflights[key]; existing != nil {
			j = existing
			c.mu.Unlock()
		} else {
			c.preflights[key] = j
			c.mu.Unlock()
			if err := c.enqueue(j, maxPreflights); err != nil {
				c.mu.Lock()
				delete(c.preflights, key)
				c.mu.Unlock()
				return PreflightResult{}, err
			}
		}
	}
	result, err := c.await(ctx, j)
	return PreflightResult{Result: result}, err
}

// enqueue queues a job, refusing it past limit jobs of its kind waiting.
// With no app connected and no runner polling lately, the job ends at once
// as unavailable.
func (c *Checker) enqueue(j *job, limit int) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	waiting := 0
	for _, q := range c.queue {
		if q.Kind == j.Kind {
			waiting++
		}
	}
	if waiting >= limit {
		c.mu.Unlock()
		return ErrBusy
	}
	now := time.Now()
	if c.opts.Subscribed != nil && !c.opts.Subscribed() && now.Sub(c.polled) > c.opts.Claim {
		c.mu.Unlock()
		c.finish(j, unavailable(j, reasonNoApp))
		return nil
	}
	j.queued = now
	c.queue = append(c.queue, j)
	c.poke()
	c.mu.Unlock()
	c.publish(j.ID, StateQueued)
	return nil
}

// await waits for a queued job's result. A job no runner claims within
// Options.Claim of being queued (or of the last poll, while a runner is
// busy with another) is unavailable; so is one still waiting after
// Options.Wait. A job ctx gives up on is taken out of the queue, unless it
// is running: its result is still kept.
func (c *Checker) await(ctx context.Context, j *job) (Result, error) {
	overall := time.NewTimer(c.opts.Wait)
	defer overall.Stop()
	claim := time.NewTimer(c.claimDeadline(j))
	defer claim.Stop()
	for {
		select {
		case <-j.done:
			return j.result, nil
		case <-ctx.Done():
			if c.dequeue(j) {
				c.finish(j, unavailable(j, "The check was cancelled."))
			}
			return Result{}, ctx.Err()
		case <-overall.C:
			if c.dequeue(j) {
				c.finish(j, unavailable(j, reasonBusy))
			}
			<-j.done
			return j.result, nil
		case <-claim.C:
			wait := c.claimDeadline(j)
			if wait > 0 {
				claim.Reset(wait)
				continue
			}
			if c.dequeue(j) {
				c.finish(j, unavailable(j, reasonNoRunner))
			}
		}
	}
}

// claimDeadline is how long a queued job may still wait for a runner: none
// once it is claimed or done.
func (c *Checker) claimDeadline(j *job) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j.claimed || !slices.Contains(c.queue, j) {
		return time.Hour
	}
	// A job runs: the runner is busy, not gone; it ends by its report or
	// by its hang timer.
	if c.running != nil {
		return c.opts.Claim
	}
	since := j.queued
	for _, at := range []time.Time{c.polled, c.freed} {
		if at.After(since) {
			since = at
		}
	}
	return time.Until(since.Add(c.opts.Claim))
}

// dequeue takes a job out of the queue, reporting whether it was there.
func (c *Checker) dequeue(j *job) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.Index(c.queue, j)
	if i < 0 {
		return false
	}
	c.queue = slices.Delete(c.queue, i, i+1)
	return true
}

// poke wakes every runner waiting in Next. It is called with mu held.
func (c *Checker) poke() {
	close(c.wake)
	c.wake = make(chan struct{})
}

// Next claims the next job, waiting up to wait for one; it reports false
// when none came. Only one job runs at a time. Claiming registers the
// candidate's frame document, so the frame route serves it to the runner,
// and starts the hang timer.
func (c *Checker) Next(ctx context.Context, wait time.Duration) (Job, bool) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		c.polled = time.Now()
		if c.closed {
			c.mu.Unlock()
			return Job{}, false
		}
		if c.running == nil && len(c.queue) > 0 {
			j := c.queue[0]
			c.queue = c.queue[1:]
			c.running, j.claimed = j, true
			c.mu.Unlock()
			if j.Kind == JobCheck {
				if _, err := c.frames.Add(j.def); err != nil {
					c.mu.Lock()
					c.running = nil
					c.poke()
					c.mu.Unlock()
					j.errors = append(j.errors, problem(CodeDefinitionInvalid, "the tile's frame document could not be made: "+strings.TrimPrefix(err.Error(), "kit: ")))
					c.finish(j, c.result(j, VerdictFail))
					continue
				}
			}
			c.mu.Lock()
			if c.running == j {
				now := time.Now()
				j.alive, j.limit = now, now.Add(c.opts.Hang+time.Duration(frames(j.Job))*frameBudget)
				j.timer = time.AfterFunc(c.opts.Hang, func() { c.expire(j) })
			}
			c.mu.Unlock()
			c.publish(j.ID, StateRunning)
			return j.Job, true
		}
		wake := c.wake
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return Job{}, false
		case <-deadline.C:
			return Job{}, false
		case <-wake:
		}
	}
}

// Report ends the running job id with the runner's report.
func (c *Checker) Report(id string, report Report) error {
	c.mu.Lock()
	j := c.running
	if j == nil || j.ID != id {
		c.mu.Unlock()
		return ErrUnknown
	}
	if j.timer != nil {
		j.timer.Stop()
	}
	c.running = nil
	c.polled, c.freed = time.Now(), time.Now()
	c.poke()
	c.mu.Unlock()
	if report.Abandoned {
		c.finish(j, unavailable(j, "The app's check runner went away before it finished; try again."))
		return nil
	}
	errs, warnings := classify(report.Problems, j.kinds)
	j.errors = append(j.errors, errs...)
	j.warnings = append(j.warnings, warnings...)
	verdict := VerdictPass
	if len(j.errors) > 0 {
		verdict = VerdictFail
	}
	result := c.result(j, verdict)
	if len(report.Runs) > maxRuns {
		report.Runs = report.Runs[:maxRuns]
	}
	for _, r := range report.Runs {
		r.Sample, r.Face, r.Size, r.Theme = clip(r.Sample, maxField), clip(r.Face, maxField), clip(r.Size, maxField), clip(r.Theme, maxField)
		result.Runs = append(result.Runs, r)
	}
	result.Engine = clip(report.Engine, maxMessage)
	c.finish(j, result)
	return nil
}

// hung ends a claimed job that got no report in time: its digest is
// refused from now on, by the frame route and every later preflight.
func (c *Checker) hung(j *job) {
	c.mu.Lock()
	if c.running != j {
		c.mu.Unlock()
		return
	}
	c.running, c.freed = nil, time.Now()
	c.poke()
	waiting := slices.Clone(c.queue)
	c.mu.Unlock()
	c.frames.MarkHung(j.Digest)
	c.publish(j.ID, StateHung)
	j.errors = append(j.errors, problem(CodeHang, "The component never finished drawing, and the app stopped answering while it drew (an endless loop?)."))
	c.finish(j, c.result(j, VerdictHang))
	// The page closes a runner that hung; the jobs still waiting need it
	// back, so they are announced again.
	for _, w := range waiting {
		c.publish(w.ID, StateQueued)
	}
}

// expire runs when a claimed job's hang timer fires: a job whose runner
// said it was alive within Options.Hang gets more time, up to its limit;
// otherwise it hung.
func (c *Checker) expire(j *job) {
	c.mu.Lock()
	if c.running != j {
		c.mu.Unlock()
		return
	}
	now := time.Now()
	deadline := j.alive.Add(c.opts.Hang)
	if j.limit.Before(deadline) {
		deadline = j.limit
	}
	if now.Before(deadline) {
		j.timer.Reset(deadline.Sub(now))
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	c.hung(j)
}

// Alive records that the runner is still drawing the running job id, which
// keeps it from being a hang for another Options.Hang. A runner whose page
// froze cannot say so: a real hang is still caught.
func (c *Checker) Alive(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	j := c.running
	if j == nil || j.ID != id {
		return ErrUnknown
	}
	j.alive = time.Now()
	c.polled = j.alive
	return nil
}

// Requeue puts the running job id back at the head of the queue, unclaimed:
// its runner never got it (the long poll's client went away).
func (c *Checker) Requeue(id string) {
	c.mu.Lock()
	j := c.running
	if j == nil || j.ID != id {
		c.mu.Unlock()
		return
	}
	if j.timer != nil {
		j.timer.Stop()
	}
	c.running, j.claimed = nil, false
	c.queue = append([]*job{j}, c.queue...)
	c.poke()
	c.mu.Unlock()
	c.publish(j.ID, StateQueued)
}

// frames is how many frames the runner draws for a job, as its matrix has
// them: for a check, each sample as a glance at each size and two details,
// and the data twice more in the dark theme; for a preflight, two.
func frames(j Job) int {
	if j.Kind == JobPreflight {
		return 2 * len(j.Samples)
	}
	n := 0
	for _, s := range j.Samples {
		n += max(len(j.Sizes), 1) + 2
		if s.Kind == KindData {
			n += 2
		}
	}
	return n
}

// finish ends a job: its result is kept, its pair's verdict recorded and
// whoever waits on it told.
func (c *Checker) finish(j *job, result Result) {
	c.mu.Lock()
	select {
	case <-j.done:
		c.mu.Unlock()
		return
	default:
	}
	j.result = result
	if j.Kind == JobPreflight {
		for key, p := range c.preflights {
			if p == j {
				delete(c.preflights, key)
			}
		}
	}
	close(j.done)
	c.mu.Unlock()
	if j.claimed && result.Verdict != VerdictHang {
		c.publish(j.ID, StateDone)
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if c.store != nil && !closed && j.dataSHA != "" && result.Verdict != VerdictUnavailable {
		if err := c.store.record(context.Background(), j.Digest, j.dataSHA, result.Verdict, time.Now()); err != nil {
			c.opts.Log.Warn("a tile check verdict could not be kept", "error", err.Error())
		}
	}
}

func (c *Checker) publish(id, state string) {
	if c.opts.Publish != nil {
		c.opts.Publish(Event{ID: id, State: state})
	}
}

// result is a job's result with the problems found so far.
func (c *Checker) result(j *job, verdict string) Result {
	errs, warnings := dedupe(j.errors), dedupe(j.warnings)
	if len(errs) > maxProblems {
		errs = errs[:maxProblems]
	}
	if len(warnings) > maxProblems {
		warnings = warnings[:maxProblems]
	}
	if errs == nil {
		errs = []Problem{}
	}
	if warnings == nil {
		warnings = []Problem{}
	}
	return Result{Check: j.ID, Digest: j.Digest, Kit: j.Kit, Verdict: verdict, Errors: errs, Warnings: warnings, Runs: []Run{}}
}

func unavailable(j *job, reason string) Result {
	warnings := dedupe(j.warnings)
	if warnings == nil {
		warnings = []Problem{}
	}
	return Result{Check: j.ID, Digest: j.Digest, Kit: j.Kit, Verdict: VerdictUnavailable, Reason: reason, Errors: []Problem{}, Warnings: warnings, Runs: []Run{}}
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "chk_" + hex.EncodeToString(b[:])
}
