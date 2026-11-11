package fix

import (
	"context"
	"fmt"
	"strings"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/untrusted"
)

// task is what a fix's prompt says about the pull request.
type task struct {
	Repository, Title, HeadRef, BaseRef string
	Number                              int
	// Conflicts are the paths the merge left conflicted; Merged says the
	// base merged without conflicts.
	Conflicts []string
	Merged    bool
	Checks    []Check
	// Threads are the review threads to address.
	Threads []Thread
	// Chat is a reviewer's conversation with the review's agent.
	Chat *ChatContext
	// Prompts replace the built-in fix prompts.
	Prompts prompt.Overrides
}

// buildPrompt is the public fix instructions followed by the data they refer
// to. Every piece of data is untrusted and sits between markers that carry a
// random nonce, which the data cannot know and so cannot close early. Secrets
// in check output and review comments are masked before they reach the model.
// Review threads go under the local ids baum gave them, outside the markers;
// the agent's answer block about them opens with a fence carrying the same
// nonce (answerFence), which is returned for parseAnswer.
func buildPrompt(ctx context.Context, t task, conflicts, checks bool) (string, string, error) {
	redactor, err := bundle.NewRedactor()
	if err != nil {
		return "", "", err
	}
	fence := untrusted.New()
	tag := fence.Tag()
	var b strings.Builder
	text, _ := prompt.FixWith(t.Prompts, conflicts, checks, len(t.Threads) > 0, t.Chat != nil && len(t.Chat.Turns) > 0)
	b.WriteString(text)
	b.WriteString("\n## Data\n\n" + fence.Notice("the repository, GitHub, the checks or reviewers") + "\n")
	block := func(heading, text string) error {
		fenced, err := fence.Masked(ctx, redactor, text)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "\n### %s\n\n%s", heading, fenced)
		return nil
	}
	pr := fmt.Sprintf("Repository: %s\nPull request: #%d\nTitle: %s\nHead branch: %s\nBase branch: %s", t.Repository, t.Number, t.Title, t.HeadRef, t.BaseRef)
	if err := block("Pull request", pr); err != nil {
		return "", "", err
	}
	if conflicts {
		text := strings.Join(t.Conflicts, "\n")
		if t.Merged {
			text = "(none: the base branch merged without conflicts)"
		}
		if err := block("Conflicted files", text); err != nil {
			return "", "", err
		}
	}
	if checks {
		if len(t.Checks) == 0 {
			if err := block("Failing checks", "(GitHub listed no failing check output)"); err != nil {
				return "", "", err
			}
		}
		for _, check := range t.Checks {
			var text strings.Builder
			fmt.Fprintf(&text, "Check: %s\nConclusion: %s\n", check.Name, check.Conclusion)
			for _, part := range []struct{ name, value string }{{"Title", check.Title}, {"Summary", check.Summary}, {"Details", check.Text}, {fmt.Sprintf("Job log, up to the end of the failed step when the log marks one (at most %d lines)", MaxLogLines), check.Log}} {
				if strings.TrimSpace(part.value) != "" {
					fmt.Fprintf(&text, "\n%s:\n%s\n", part.name, part.value)
				}
			}
			if err := block("Failing check", text.String()); err != nil {
				return "", "", err
			}
		}
	}
	if t.Chat != nil {
		if t.Chat.Omitted > 0 {
			fmt.Fprintf(&b, "\n(%d earlier %s of the conversation %s left out.)\n", t.Chat.Omitted, plural(t.Chat.Omitted, "turn", "turns"), plural(t.Chat.Omitted, "was", "were"))
		}
		for _, turn := range t.Chat.Turns {
			if err := block(fmt.Sprintf("Conversation turn %d", turn.Seq), chatTurnText(turn)); err != nil {
				return "", "", err
			}
		}
	}
	for _, thread := range t.Threads {
		if err := block("Review thread "+thread.Ref, threadText(thread)); err != nil {
			return "", "", err
		}
	}
	if len(t.Threads) > 0 {
		fmt.Fprintf(&b, "\n## Answer block\n\nOpen the answer block with exactly this line:\n\n    %s\n\nand close it with a line of three backticks.\n", answerFence(tag))
	}
	return b.String(), tag, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
