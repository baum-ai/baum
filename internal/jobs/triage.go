package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// ReasonNoSignificantChange completes an automatic review of a new head
// without a new version: with automatic reviews set to review on change
// (repos.UpdatesOnChange), the review agent read what the head changed since
// the saved review and found nothing that would make a difference to it. The
// job's Summary is the agent's reason.
const ReasonNoSignificantChange = "no_significant_change"

// UnchangedError is Execute's answer for an automatic review that triage
// stood down (ReasonNoSignificantChange). Reason is the agent's, in one or
// two sentences.
type UnchangedError struct{ Reason string }

func (e *UnchangedError) Error() string { return "review still applies: " + e.Reason }

// maxTriageDiffBytes bounds the diff triage reads. A larger change is
// reviewed again without asking: it is unlikely to leave the review as it is.
const maxTriageDiffBytes = 200 << 10

// maxTriageReviewBytes bounds the saved review's outline triage reads.
const maxTriageReviewBytes = 40 << 10

var triageSchema = []byte(`{
  "type": "object",
  "properties": {
    "regenerate": {"type": "boolean"},
    "reason": {"type": "string"}
  },
  "required": ["regenerate", "reason"],
  "additionalProperties": false
}`)

type triageVerdict struct {
	Regenerate bool   `json:"regenerate"`
	Reason     string `json:"reason"`
}

// triageInput is what triage needs from Execute.
type triageInput struct {
	prefs    repos.Preferences
	versions interface {
		Latest(context.Context, store.Key) (store.Version, error)
		KeepHead(context.Context, store.Key, int, string, string, string, time.Time) error
	}
	key   store.Key
	clone interface {
		FetchPulls(context.Context, string, []gitlocal.PullRequest) error
		Diff(context.Context, string, string, int64) ([]byte, error)
	}
	p       Prepared
	backend agent.Backend
	// system is the prompt (prompt.Triage), the person's custom one when
	// they replaced it.
	system string
}

// triage decides, for an automatic review of a new head while automatic
// reviews are set to review on change, whether the head is worth a new
// review. It returns an UnchangedError when it is not, nil when the review
// should be written: also whenever triage cannot decide (no saved review, a
// diff too large or unreadable, a backend without single calls, a failed
// call), since a review written when it was not needed costs less than one
// missed.
func triage(ctx context.Context, in triageInput) error {
	if in.prefs.ReviewUpdates() != repos.UpdatesOnChange {
		return nil
	}
	latest, err := in.versions.Latest(ctx, in.key)
	if err != nil || latest.Metadata.Head == "" || latest.Metadata.Head == in.p.HeadSHA {
		return nil
	}
	completer, ok := in.backend.(agent.Completer)
	if !ok {
		return nil
	}
	outline, err := reviewOutline(latest.Document)
	if err != nil {
		return nil
	}
	// The saved review's head is kept in the clone (reviewrefs.Keep); the
	// new head is fetched like the review would fetch it.
	_ = in.clone.FetchPulls(ctx, "", []gitlocal.PullRequest{{Number: in.p.PR, BaseSHA: in.p.BaseSHA, HeadSHA: in.p.HeadSHA}})
	diff, err := in.clone.Diff(ctx, latest.Metadata.Head, in.p.HeadSHA, maxTriageDiffBytes)
	if err != nil {
		return nil
	}
	// The saved version is recorded as current at the new head, so the pull
	// request does not show it as outdated. A failed write leaves it shown as
	// outdated, as before, and still writes no review.
	keep := func(reason string) error {
		_ = in.versions.KeepHead(context.WithoutCancel(ctx), in.key, latest.Metadata.Version, in.p.HeadSHA, in.p.BaseSHA, reason, time.Now().UTC())
		return &UnchangedError{Reason: reason}
	}
	if len(strings.TrimSpace(string(diff))) == 0 {
		// Same tree, such as a rebase onto an unchanged base or an empty
		// commit: nothing the review covers moved.
		return keep("The new commits change no files since the review.")
	}
	// The diff is raw git output: its secrets are masked as a review's
	// patches are (bundle.Redact) before the model reads it. A diff that
	// cannot be masked is not sent, and the review is written.
	redactor, err := bundle.NewRedactor()
	if err != nil {
		return nil
	}
	masked, _, err := redactor.RedactString(ctx, string(diff))
	if err != nil {
		return nil
	}
	user := fmt.Sprintf("<review>\n%s\n</review>\n\n<diff>\n%s\n</diff>", outline, masked)
	completion, err := completer.Complete(ctx, agent.CompleteRequest{System: in.system, User: user, Schema: triageSchema, MaxOutputTokens: 1024})
	if err != nil {
		return nil
	}
	var verdict triageVerdict
	if json.Unmarshal(completion.Document, &verdict) != nil || verdict.Regenerate {
		return nil
	}
	// The reason becomes the job's summary: whatever of the diff the model
	// echoes is masked again, and an unmaskable one is replaced.
	reason, _, err := redactor.RedactString(ctx, strings.TrimSpace(verdict.Reason))
	if err != nil || reason == "" {
		reason = "The new commits don't change what the review says."
	}
	return keep(truncate(reason, maxSummary))
}

// reviewOutline is the part of a stored review triage reads: the overview,
// and each section's title, summary and active risks.
func reviewOutline(document []byte) (string, error) {
	var stored struct {
		Summary struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"summary"`
		Sections []struct {
			Title   string `json:"title"`
			Summary struct {
				Text string `json:"text"`
			} `json:"summary"`
			Risks []struct {
				Tier  string `json:"tier"`
				Text  string `json:"text"`
				State string `json:"state"`
			} `json:"risks"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(document, &stored); err != nil {
		return "", err
	}
	if stored.Summary.Text == "" && len(stored.Sections) == 0 {
		return "", errors.New("review has no outline")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Overview: %s\n%s\n", stored.Summary.Title, stored.Summary.Text)
	for _, section := range stored.Sections {
		fmt.Fprintf(&b, "\nSection: %s\n%s\n", section.Title, section.Summary.Text)
		for _, risk := range section.Risks {
			if risk.State != "" && risk.State != "active" {
				continue
			}
			fmt.Fprintf(&b, "- Risk (%s): %s\n", risk.Tier, risk.Text)
		}
		if b.Len() > maxTriageReviewBytes {
			break
		}
	}
	return truncate(b.String(), maxTriageReviewBytes), nil
}
