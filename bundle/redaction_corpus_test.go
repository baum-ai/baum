package bundle

import "strings"

// The redaction corpus: representative synthetic inputs for every kind of
// bundle text, each placed in the bundle field it would arrive in. Every
// credential below is a made-up value in the provider's format (most are
// goredact's own public accuracy fixtures); none is live. Provider prefixes
// are split with + and other values kept in pieces, so the source does not
// itself trip a secret scanner.
var (
	corpusAWSKeyID      = "AKIA" + "UJZDEGXDNCF32EPF"
	corpusAWSSecret     = pieces("fKm/r5", "kJP1Vr", "T+1FJo", "rs/6IL", "i8IHn5", "kxsC7t", "VO/H")
	corpusSlackBot      = "xox" + "b-0265423511615-5940781618495-KmTecQoXsf2o3gyrDO1xkxwnQrS7RP"
	corpusStripe        = "sk_" + "live_oHBvRPOIvGrv5iFlbCBFNOgmBjMtpsia"
	corpusOpenAI        = "sk-" + "proj-BVNMKlvU9FzBvWdqOPDZtJVcBIS5TvkvuzSLy8asT3BlbkFJLq83zMskErFlHpuT9KMBRkPgrihZPskJwst1ctDX"
	corpusAnthropic     = "sk-" + "ant-api03-L6VtNHDzbvOYIs52KThe1DBzYMxIsTnjxqnzKJgS1niDgzJKhy20RHoBqBbuVcPjhcUAwu89LeznSyn2bpa26sQIrfAA"
	corpusGitLab        = "glp" + "at-BO_yY1vy1Dx71XJWCS_eOE50i2nLmR"
	corpusNPM           = "npm" + "_plknz1kJAShTH8KvwDZPcixBekByPBAakTBO"
	corpusHuggingFace   = "hf_" + "nW2OPJ0rGsD6y4OUtEXQxmAa3BItWMoP6J"
	corpusLinear        = "lin" + "_api_FeWaVUqG2KVasfSq8Z0wjCdFUQUHxZ3g0Aq3idaD"
	corpusFineGrained   = "github" + "_pat_pNLXUQBEWt9iBIWhqsMcTQ_iT0f4QyUrymftnlmT2umhmf7J6BgIuQ3ynO4SNP6dVUK0CcwZUvP8pmGW9F"
	corpusGCP           = "AIza" + "zyN9zHYIa4UOrGNATMuDJawTgsu8PO_799n"
	corpusSendGrid      = "SG." + "OhWhEN3so3OxYgF3AZu3Iq.oPmn0pzlQY1wWmzAmka3p744b8VKkqLencZSDFf8J61"
	corpusVault         = "hv" + "s.ld4Ki-Hfk1htnZr3dfRKIbAPeW6N9c54fl9W2Fmz9c238OBYbOQjsfVeZdWc45s-4D3q2KyKXcJmNUJ4o7L_-RfvuZfhMuTfHRLu"
	corpusPyPI          = "pypi" + "-AgEIcHlwaS5vcmciP1aKP8I598r9hDBybMnEIx9dj0L-mNSWyd04f1Ibz0rFbGOnIRyMex-3LajVwjAeU-5aeC_7TS"
	corpusDatadog       = "af7c06c09c36c278" + "cc2b6013ddcdb1fa"
	corpusJWT           = "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." + "ey" + "JzdWIiOiJzdmMtcmVwb3J0aW5nIiwiaXNzIjoiaHR0cHM6Ly9hdXRoLmludGVybmFsLmV4YW1wbGUiLCJleHAiOjE3NjcyMjU2MDB9.q4TnV7bXk2LwZ9pR0sYhF6dJ1aG5uEiO8cM3rTvQ6wN"
	corpusAzureConn     = "DefaultEndpointsProtocol=https;AccountName=contosostorage;AccountKey=" + "5suKcNd8Zra9A9sKPxZ9W3qLy7zKUVQDT7S8sTQCBNR3YbDgbleph1QHt61QTC4XATWS8PHp9NHfYjFM5DI4pZ==;EndpointSuffix=core.windows.net"
	corpusPEMBody       = "rpvGMXOAKGX6B+kpekdCsOtzCoV+ZuCNdBcPTT9hA6rOjKjGc6MefdDU8p5oXcY7\nrmqoDH0qVi/huXgpY1qHTa43gddwT5TXFrrNosHPdl19o2Z+/7piAa2GqytsZN3C\n2uCmH6CHd67YEB0uS5MMuWrzaXnJ0gfBwd91bSrUJj9LZRsK1PbRQUyZJjzlxYuw\n75dnx5rqKXyLS8Zhms26Wg=="
	corpusPEM           = "-----BEGIN OPENSSH " + "PRIVATE KEY-----\n" + corpusPEMBody + "\n-----END OPENSSH PRIVATE KEY-----"
	corpusDBPassword    = pieces("wZ8kQ3", "vR7pL4", "mN2x")
	corpusCookieSession = pieces("aK9mQ2", "vR7wL4", "mN8pX1", "sTbF3j")
	corpusPassword      = pieces("Xk9mP2", "vQ7Rt4", "Ws8Lb")
	corpusFlagPassword  = pieces("tR8kW3", "nQ7zXm", "2")
	corpusBearer        = pieces("nQ7wL4", "mX8pZ1", "sT6bK3", "jH0cV5", "yR2dF9", "gA")
	corpusHeaderBearer  = pieces("kJ8vQ2", "mZ7xW4", "pL9sN3", "tYbF6h", "R0cD5g", "U1aE")
)

// pieces joins a value kept in short pieces, so no literal in this file
// reads as a credential to a secret scanner.
func pieces(parts ...string) string { return strings.Join(parts, "") }

// prefixLines prefixes every line of text, as a diff marks added lines.
func prefixLines(prefix, text string) string {
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
}

type corpusItem struct {
	name, field, text string
}

// redactionCorpus is ordered by kind of input; the name is the golden file.
var redactionCorpus = []corpusItem{
	{"prose-clean", "pull_request.body", "Refactor the retry loop so a failed upload is retried twice.\n\nNo behaviour change for successful uploads."},
	{"prose-near-misses", "pull_request.body", "Set `ANTHROPIC_API_KEY=sk-ant-xxxxxxxx` locally, or `Authorization: Bearer <token>`,\nor export API_TOKEN=${API_TOKEN}. The value ghp_short is not a token and AKIA-short is not a key."},
	{"prose-provider-tokens", "pull_request.body", strings.Join([]string{
		"Rotated every credential that leaked in the CI log:",
		"",
		"- AWS: AWS_ACCESS_KEY_ID=" + corpusAWSKeyID + " and AWS_SECRET_ACCESS_KEY=" + corpusAWSSecret,
		"- GitHub classic " + plantedSecret + " and fine-grained " + corpusFineGrained,
		"- Slack bot " + corpusSlackBot,
		"- Stripe " + corpusStripe,
		"- OpenAI OPENAI_API_KEY=" + corpusOpenAI,
		"- Anthropic " + corpusAnthropic,
		"- GitLab " + corpusGitLab + ", npm " + corpusNPM + ", Hugging Face " + corpusHuggingFace,
		"- Linear " + corpusLinear + ", Google GOOGLE_API_KEY=" + corpusGCP,
		"- SendGrid " + corpusSendGrid,
		"- Vault VAULT_TOKEN=" + corpusVault,
		"- PyPI " + corpusPyPI + ", Datadog DD_API_KEY=" + corpusDatadog,
	}, "\n")},
	{"prose-title-token", "pull_request.title", "Revoke leaked token " + plantedSecret},
	{"multiline-pem-comment", "conversation.body", "The fixture still embeds a real-looking key:\n\n```\n" + corpusPEM + "\n```\n\nPlease load it from the secret store instead."},
	{"multiline-json-secrets", "session.prompt", "Use this config for the integration test:\n{\n  \"database\": {\n    \"password\": \"" + corpusDBPassword + "\",\n    \"url\": \"postgres://svc_user:" + corpusDBPassword + "@db.internal:5432/app\"\n  },\n  \"api_key\": \"" + corpusOpenAI + "\",\n  \"assertion\": \"" + corpusJWT + "\",\n  \"storage\": \"" + corpusAzureConn + "\"\n}"},
	{"multiline-escaped-key", "session.reply", `I found the service account file: {"type":"service_account","private_key":"-----BEGIN ` + `PRIVATE KEY-----\n` + strings.ReplaceAll(corpusPEMBody, "\n", `\n`) + `\n-----END PRIVATE KEY-----\n","client_email":"ci-runner@example-project.iam.gserviceaccount.com"}`},
	{"urls-with-credentials", "pull_request.body", strings.Join([]string{
		"The staging DSN was postgres://svc_user:" + corpusDBPassword + "@db.internal:5432/app and the cache redis://:" + corpusCookieSession + "@cache.internal:6379/0.",
		"Webhook: https://hooks.example.test/services/T000/B000/callback?access_token=" + plantedSecret + "&channel=ci",
		"Download: https://deploy:" + corpusDBPassword + "@artifacts.example.test/build.tar.gz",
		"Signed URL: https://storage.example.test/b/o?X-Goog-Signature=abc123&token=" + corpusHuggingFace,
		"Plain link with no secret: https://example.test/docs?page=2",
	}, "\n")},
	{"check-url-token", "check.url", "https://ci.example.test/run/42?token=" + plantedSecret},
	{"emails-various", "conversation.body", strings.Join([]string{
		"Ping ada.lovelace@example.com or Grace <grace.hopper+ci@mail.example.co.uk>.",
		"Escalations: SECURITY-TEAM@Example.ORG, mailto:ops@example.net?subject=leak.",
		"Sentence end: alan@example.com. Comma: kat@example.io, then more.",
		"Git trailer style: Co-authored-by: Linus <torvalds@example.org>",
		"Long local part: " + strings.Repeat("a", 70) + "@example.com",
		"Not addresses: user@localhost, a @ b, host@10.0.0.1, @scope/pkg, x@y",
	}, "\n")},
	{"emails-partial-domain", "conversation.body", "contact ada@example.com.1 now\nada@example.com-1, ada@example.com.v2 and ada@example.co.uk2\nada@-example.com or ada@.example.com, not ada@example.com_x"},
	{"mentions-prose", "conversation.body", "@alice can you take a look? cc @bob-smith and @acme/platform-team.\nThanks @carol, see https://github.com/@dave and pkg/@scope/name."},
	{"mentions-and-email-title", "pull_request.title", "Fix login for @alice (reported by eve@example.com)"},
	{"review-suggestion-code", "conversation.body", "Override the base method here:\n```suggestion\n    @Override\n    public String token() { return \"" + corpusStripe + "\"; }\n```\n@alice FYI"},
	{"patch-code-annotations", "diff.patch", strings.Join([]string{
		"@@ -10,6 +10,12 @@ class Service:",
		"+    @property",
		"+    @functools.cache",
		"+    def client(self):",
		"+        return Client()",
		" # helpers",
		"-import \"@types/node\"",
		"+import type { X } from \"@acme/sdk\";",
		"+@media (max-width: 600px) { .a { color: red } }",
		"+    @Override",
		"+    @SuppressWarnings(\"unchecked\")",
	}, "\n")},
	{"patch-secrets", "diff.patch", strings.Join([]string{
		"@@ -1,4 +1,6 @@",
		" # settings",
		"-AWS_ACCESS_KEY_ID=" + corpusAWSKeyID,
		"+AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID}",
		"+SLACK_TOKEN = \"" + corpusSlackBot + "\"",
		"+DATABASE_URL=postgres://svc_user:" + corpusDBPassword + "@db.internal:5432/app",
		"+export PASSWORD=" + corpusPassword,
		" DEBUG = False",
	}, "\n")},
	{"patch-emails-partial-domain", "diff.patch", "@@ -1,3 +1,3 @@\n+ada@example.com.1\n ada@-example.com\n-ada@.example.com\n+ada@example.co.uk2"},
	{"patch-multiline-pem", "diff.patch", "@@ -0,0 +1,6 @@\n" + prefixLines("+", corpusPEM)},
	{"patch-multiline-pem-context", "diff.patch", "@@ -3,8 +3,8 @@\n key = \"\"\"\n" + prefixLines(" ", corpusPEM) + "\n-\"\"\"\n+\"\"\".strip()"},
	{"patch-emails", "diff.patch", strings.Join([]string{
		"@@ -1,3 +1,4 @@",
		" Ada Lovelace <ada.lovelace@example.com>",
		"-Grace Hopper <grace@example.org>",
		"+Grace Hopper <grace.hopper@example.org>",
		"+# contact security@example.com for disclosures.",
	}, "\n")},
	{"commit-subject-token", "commit.subject", "Remove hard-coded Stripe key " + corpusStripe},
	{"commit-subject-scoped-package", "commit.subject", "Bump @types/node from 20.1.0 to 22.4.1"},
	{"commit-subject-mention-email", "commit.subject", "Apply review from @alice <alice@example.com>"},
	{"review-comment-secret", "conversation.body", "This line leaks `" + corpusGitLab + "`; also the Authorization: Bearer " + corpusHeaderBearer + " header is logged."},
	{"session-prompt-prose", "session.prompt", "Deploy with VAULT_TOKEN=" + corpusVault + " and email the result to release-bot@example.com, then tell @alice."},
	{"session-title", "session.title", "Fix outage using token " + plantedSecret},
	{"command-target-flag", "command.target", "mysql -h db.prod.internal -u app --password=" + corpusFlagPassword + " app_db"},
	{"command-target-bearer", "command.target", "curl -H 'Authorization: Bearer " + corpusBearer + "' https://api.example.com/v1"},
	{"command-target-cookie", "command.target", "curl -H 'Cookie: theme=dark; sessionid=" + corpusCookieSession + "; lang=en' https://app.example.test/"},
	{"reference-authorship", "reference.value", "Generated with an assistant; questions to bot-owner@example.com, key " + corpusAnthropic},
	{"attachment-alt", "attachment.alt", "Screenshot of the settings page showing " + corpusSendGrid + " for @alice"},
	{"head-ref-token", "pull_request.head_ref", "fix/rotate-" + plantedSecret},
}
