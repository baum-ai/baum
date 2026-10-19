package baumcli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	locale "github.com/jeandeaual/go-locale"
	"github.com/mattn/go-isatty"

	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/vendors"
)

var getUserLocale = locale.GetLocale

type uploadSessionSummary struct {
	label    string
	count    int
	earliest time.Time
	latest   time.Time
}

func writeUploadSessionSummary(w io.Writer, sessions []collector.Session) {
	writeUploadSessionSummaryWithLayout(w, sessions, userDateLayout())
}

func writeUploadSessionSummaryWithLayout(w io.Writer, sessions []collector.Session, layout string) {
	bySource := make(map[vendors.Slug]*uploadSessionSummary)
	for _, session := range sessions {
		summary := bySource[session.Source]
		if summary == nil {
			summary = &uploadSessionSummary{label: uploadSourceLabel(session.Source)}
			bySource[session.Source] = summary
		}
		summary.count++
		if summary.earliest.IsZero() || session.ModifiedAt.Before(summary.earliest) {
			summary.earliest = session.ModifiedAt
		}
		if summary.latest.IsZero() || session.ModifiedAt.After(summary.latest) {
			summary.latest = session.ModifiedAt
		}
	}

	summaries := make([]uploadSessionSummary, 0, len(bySource))
	for _, source := range []vendors.Slug{vendors.Codex, vendors.ClaudeCode, vendors.Cursor} {
		if summary := bySource[source]; summary != nil {
			summaries = append(summaries, *summary)
			delete(bySource, source)
		}
	}
	otherSummaries := make([]uploadSessionSummary, 0, len(bySource))
	for _, summary := range bySource {
		otherSummaries = append(otherSummaries, *summary)
	}
	sort.Slice(otherSummaries, func(i, j int) bool { return otherSummaries[i].label < otherSummaries[j].label })
	summaries = append(summaries, otherSummaries...)

	_, _ = fmt.Fprintf(w, "Found %d agent %s:\n", len(sessions), sessionNoun(len(sessions)))
	for _, summary := range summaries {
		_, _ = fmt.Fprintf(w, "  - %s: %d (%s)\n", summary.label, summary.count, formatUploadDateRange(summary, layout))
	}
}

func sessionNoun(count int) string {
	if count == 1 {
		return "session"
	}
	return "sessions"
}

func fileNoun(count int) string {
	if count == 1 {
		return "file"
	}
	return "files"
}

// skippedFileLimit caps how many unreadable files are named individually. A
// store-wide breakage skips every file for the same reason, and a thousand
// identical lines hide the reason rather than showing it.
const skippedFileLimit = 5

// writeSkippedSessionFiles names the files discovery dropped. Sessions were
// still found, so this is a warning beside a successful upload rather than a
// failure — but the dropped files are named, because the alternative is an
// upload that silently lacks them.
func writeSkippedSessionFiles(w io.Writer, report collector.ScanReport) {
	if len(report.Skipped) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "Skipped %d unreadable session %s:\n", len(report.Skipped), fileNoun(len(report.Skipped)))
	for _, skipped := range report.Skipped[:min(len(report.Skipped), skippedFileLimit)] {
		_, _ = fmt.Fprintf(w, "  - %s: %s\n", skipped.Path, skipped.Reason)
	}
	if extra := len(report.Skipped) - skippedFileLimit; extra > 0 {
		_, _ = fmt.Fprintf(w, "  ... and %d more\n", extra)
	}
}

// unreadableSessionsError is the empty-result case where the store was not
// empty: every session file in it failed to read.
func unreadableSessionsError(report collector.ScanReport) error {
	return fmt.Errorf("no readable agent sessions: %d session %s could not be read, first %s: %s",
		len(report.Skipped), fileNoun(len(report.Skipped)), report.Skipped[0].Path, report.Skipped[0].Reason)
}

func uploadSourceLabel(source vendors.Slug) string {
	switch source {
	case vendors.Codex:
		return "Codex"
	case vendors.ClaudeCode:
		return "Claude Code"
	case vendors.Cursor:
		return "Cursor"
	default:
		return string(source)
	}
}

func formatUploadDateRange(summary uploadSessionSummary, layout string) string {
	earliest := summary.earliest.In(time.Local).Format(layout)
	latest := summary.latest.In(time.Local).Format(layout)
	if earliest == latest {
		return earliest
	}
	return earliest + " - " + latest
}

func userDateLayout() string {
	userLocale, err := getUserLocale()
	if err != nil {
		return time.DateOnly
	}
	return dateLayoutForLocale(userLocale)
}

func dateLayoutForLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" || strings.EqualFold(locale, "C") || strings.HasPrefix(strings.ToUpper(locale), "C.") ||
		strings.EqualFold(locale, "POSIX") {
		return time.DateOnly
	}
	locale = strings.SplitN(locale, ".", 2)[0]
	locale = strings.SplitN(locale, "@", 2)[0]
	parts := strings.FieldsFunc(locale, func(r rune) bool { return r == '_' || r == '-' })
	region := ""
	if len(parts) > 1 {
		region = strings.ToUpper(parts[len(parts)-1])
	}
	switch region {
	case "US", "PH":
		return "01/02/2006"
	case "CN", "JP", "KR", "TW":
		return "2006/01/02"
	case "CA", "HU", "LT", "SE":
		return time.DateOnly
	case "AT", "BG", "CH", "CZ", "DE", "EE", "FI", "HR", "LV", "PL", "RO", "RU", "SI", "SK", "UA":
		return "02.01.2006"
	default:
		return "02/01/2006"
	}
}

// promptInput shares one buffered reader across prompts and remembers whether
// the stream under it is a terminal.
type promptInput struct {
	*bufio.Reader
	terminal bool
}

func newPromptInput(in io.Reader) *promptInput {
	if p, ok := in.(*promptInput); ok {
		return p
	}
	return &promptInput{Reader: bufio.NewReader(in), terminal: isTerminalReader(in)}
}

func confirmUpload(in io.Reader, out io.Writer) (bool, error) {
	prompt := newPromptInput(in)
	reader := prompt.Reader
	for {
		_, _ = fmt.Fprint(out, "Continue? [y/N] ")
		answer, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("reading confirmation: %w", err)
		}
		if !prompt.terminal {
			_, _ = fmt.Fprintln(out)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		default:
			_, _ = fmt.Fprintln(out, "Please answer yes or no.")
			if errors.Is(err, io.EOF) {
				return false, nil
			}
		}
	}
}

type preparationProgress struct {
	writer      io.Writer
	total       int
	interactive bool
}

func newPreparationProgress(writer io.Writer, total int) *preparationProgress {
	return &preparationProgress{writer: writer, total: total, interactive: isTerminalWriter(writer)}
}

func (p *preparationProgress) Start() {
	if p.interactive {
		p.render(0)
		return
	}
	_, _ = fmt.Fprintf(p.writer, "Preparing %d %s for upload...\n", p.total, sessionNoun(p.total))
}

func (p *preparationProgress) Update(completed, _ int) {
	if p.interactive {
		p.render(completed)
	}
}

func (p *preparationProgress) Finish() {
	if p.interactive {
		p.render(p.total)
		_, _ = fmt.Fprintln(p.writer)
		return
	}
	_, _ = fmt.Fprintf(p.writer, "Prepared %d %s for upload.\n", p.total, sessionNoun(p.total))
}

func (p *preparationProgress) Abort() {
	if p.interactive {
		_, _ = fmt.Fprintln(p.writer)
	}
}

func (p *preparationProgress) render(completed int) {
	const width = 24
	filled := 0
	if p.total > 0 {
		filled = completed * width / p.total
	}
	bar := strings.Repeat(" ", width)
	if filled > 0 {
		bar = strings.Repeat("=", filled-1) + ">" + strings.Repeat(" ", width-filled)
	}
	_, _ = fmt.Fprintf(
		p.writer,
		"\rPreparing sessions for upload [%s] %d/%d",
		bar,
		completed,
		p.total,
	)
}

type fdWriter interface {
	Fd() uintptr
}

func isTerminalWriter(writer io.Writer) bool {
	return isTerminalStream(writer)
}

func isTerminalReader(reader io.Reader) bool {
	return isTerminalStream(reader)
}

func isTerminalStream(stream any) bool {
	file, ok := stream.(fdWriter)
	if !ok {
		return false
	}
	fd := file.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}
