package fix

import "testing"

func TestHeadline(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"plain", "Rename this.\nIt reads badly.", "Rename this."},
		{"codex badge", "**<sub><sub>![P2 Badge](https://img.shields.io/badge/P2-yellow?style=flat)</sub></sub>  Reset walkthrough state when the review version changes**\n\nWhen the version changes the state is kept.\n\nUseful? React with 👍 / 👎.", "Reset walkthrough state when the review version changes"},
		{"coderabbit", "<!-- coderabbit-ai-generated:v1 -->\n_⚠️ Potential issue_ | _🟠 Major_\n\n**Handle the error from `fetchFollows`.**\n\nThe hook swallows it.\n\n<details>\n<summary>🤖 Prompt for AI Agents</summary>\n\n```\nIn apps/mobile/hooks/api/useFollowRequests.ts ...\n```\n</details>", "Handle the error from fetchFollows."},
		{"multi-line html comment", "<!-- generated\nby a bot -->\nUse a constant here.", "Use a constant here."},
		{"unterminated html comment", "<!-- nothing else", ""},
		{"link and code", "> See [the docs](https://example.com) about `ctx`.", "See the docs about ctx."},
		{"code block first", "```suggestion\nreturn nil\n```\nThis is simpler.", "This is simpler."},
		{"emphasis kept apart from names", "Use *snake_case* in __init__ files", "Use snake_case in init files"},
		{"heading", "## Bug\nDetails", "Bug"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Headline(c.body); got != c.want {
				t.Fatalf("Headline = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPlainText(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"plain", "Rename this.\nIt reads badly.", "Rename this.\nIt reads badly."},
		{"codex badge", "**<sub><sub>![P2 Badge](https://img.shields.io/badge/P2-yellow?style=flat)</sub></sub>  Re-arm polling**\n\nIt stops after `read` fails.\n\n\nUseful? React with 👍 / 👎.\n\n<!-- codex-code-review-finding:v1 -->", "Re-arm polling\n\nIt stops after read fails.\n\nUseful? React with 👍 / 👎."},
		{"lists", "Two things:\n- a [link](https://example.com)\n* *b*\n1. first\n2) second", "Two things:\n• a link\n• b\n1. first\n2) second"},
		{"code block kept", "Try:\n```go\nif err != nil {\n\treturn err\n}\n```\nDone.", "Try:\nif err != nil {\n    return err\n}\nDone."},
		{"heading and quote", "## Bug\n> quoted", "Bug\nquoted"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := PlainText(c.body); got != c.want {
				t.Fatalf("PlainText = %q, want %q", got, c.want)
			}
		})
	}
}
