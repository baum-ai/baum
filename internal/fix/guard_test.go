package fix

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/github"
)

// Fake credentials the redaction engine takes for secrets, split so the
// source holds none whole.
const (
	fakeAWSKey     = "AKIA" + "UJZDEGXDNCF32EPF"
	fakePrivateKey = "-----BEGIN OPENSSH " + "PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
		"QyNTUxOQAAACDhd8eLGkS1bpvvuSeYgM3fFHjyFNbMHQHpS28jUPNLdQAAAJiS7YIbku2C\n" +
		"GwAAAAtzc2gtZWQyNTUxOQAAACDhd8eLGkS1bpvvuSeYgM3fFHjyFNbMHQHpS28jUPNLdQ\n" +
		"-----END OPENSSH " + "PRIVATE KEY-----\n"
)

// writeIn writes name under dir, making its directories.
func writeIn(dir, name, content string) error {
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// guardOptions are a fix whose agent runs write in the checkout, of review
// comments when threads is set, else of the failing checks.
func guardOptions(t *testing.T, r Repositories, threads bool, write func(dir string) error) Options {
	o := options(t, r, &fakeAgent{fix: func(_ context.Context, dir string) error { return write(dir) }})
	if threads {
		o.Threads = []Thread{{Ref: "T1", ID: "RT_1", Path: "a.txt", Line: 1, Comments: []github.ThreadComment{member("alice", "Rename it.")}}}
		o.Replies = &fakeThreads{states: map[string]github.ThreadState{"RT_1": openThread()}}
	} else {
		o.Checks = true
	}
	return o
}

func pushedHead(t *testing.T, r Repositories) bool {
	t.Helper()
	return gitIn(t, r.Remote, "rev-parse", "refs/heads/feature") != r.Head
}

// A fix never pushes a credential the agent copied into the checkout: a
// secret on a line it added, or a file at a credential's usual place.
func TestFixRefusesToPushACredential(t *testing.T) {
	for _, test := range []struct {
		name, path, content, named string
	}{
		{"an AWS key in code", "config.go", "package config\n\nconst key = \"" + fakeAWSKey + "\"\n", "config.go"},
		{"a private key file", "keys/deploy.pem", fakePrivateKey, "keys/deploy.pem"},
		{"an SSH key by its name", "home/.ssh/id_ed25519", "not a key at all\n", "home/.ssh/id_ed25519"},
		{"the AWS CLI's credentials", ".aws/credentials", "[default]\n", ".aws/credentials"},
		{"the GitHub CLI's hosts", "copy/.config/gh/hosts.yml", "github.com:\n  user: someone\n", "copy/.config/gh/hosts.yml"},
		{"a git credential store", ".git-credentials", "https://example.com\n", ".git-credentials"},
		{"a secret in a file's name", "notes/" + fakeAWSKey + "/.netrc", "machine example.com\n", "notes/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := NewRepositories(t)
			o := guardOptions(t, r, false, func(dir string) error { return writeIn(dir, test.path, test.content) })
			_, err := Run(context.Background(), o)
			if reasonOf(err) != ReasonCredentialAdded || !strings.Contains(err.Error(), test.named) {
				t.Fatalf("err %v (reason %q)", err, reasonOf(err))
			}
			if strings.Contains(err.Error(), fakeAWSKey) || strings.Contains(err.Error(), "PRIVATE KEY") {
				t.Fatalf("the refusal holds the secret: %v", err)
			}
			if pushedHead(t, r) {
				t.Fatal("the credential was pushed")
			}
			checkCleanedUp(t, r, o)
		})
	}
}

// What the pull request already held, and what the redaction engine masks
// that is no secret, does not stop a fix.
func TestFixPushesWhatAddsNoCredential(t *testing.T) {
	r := NewRepositories(t)
	// The pull request itself has a key in a test fixture.
	gitIn(t, r.Seed, "checkout", "-q", "feature")
	writeFile(t, r.Seed, "fixture.txt", "key = "+fakeAWSKey+"\n")
	gitIn(t, r.Seed, "add", "-A")
	gitIn(t, r.Seed, "commit", "-q", "-m", "fixture")
	r.Head = gitIn(t, r.Seed, "rev-parse", "HEAD")
	gitIn(t, r.Seed, "push", "-q", "origin", "feature")
	o := guardOptions(t, r, false, func(dir string) error {
		if err := writeIn(dir, "fixture.txt", "# a fixture\nkey = "+fakeAWSKey+"\n"); err != nil {
			return err
		}
		return writeIn(dir, "AUTHORS", "Ada <ada@example.com>\n")
	})
	o.HeadSHA = r.Head
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !pushedHead(t, r) {
		t.Fatal("nothing was pushed")
	}
}

// A repository the agent made in the checkout, beside a change of its own,
// is never pushed as a gitlink.
func TestFixRefusesANestedRepository(t *testing.T) {
	r := NewRepositories(t)
	o := guardOptions(t, r, false, func(dir string) error {
		if err := writeIn(dir, "a.txt", "fixed\n"); err != nil {
			return err
		}
		nested := filepath.Join(dir, "vendor", "lib")
		if err := writeIn(nested, "x.txt", "x\n"); err != nil {
			return err
		}
		for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "nested"}} {
			gitIn(t, nested, args...)
		}
		return nil
	})
	_, err := Run(context.Background(), o)
	if reasonOf(err) != ReasonNestedRepository || !strings.Contains(err.Error(), "vendor/lib") {
		t.Fatalf("err %v (reason %q)", err, reasonOf(err))
	}
	if pushedHead(t, r) {
		t.Fatal("the gitlink was pushed")
	}
}

// A fix whose prompt held review comments or a conversation never pushes a
// workflow change, whatever else it fixes; one of the checks alone may.
func TestFixRefusesAWorkflowChangeWithReviewComments(t *testing.T) {
	addWorkflow := func(dir string) error {
		return writeIn(dir, ".github/workflows/ci.yml", "on: push\njobs: {}\n")
	}
	resolveAndAddWorkflow := func(dir string) error {
		if err := writeIn(dir, "a.txt", "feature and main\n"); err != nil {
			return err
		}
		return addWorkflow(dir)
	}
	for _, test := range []struct {
		name              string
		threads           bool
		conflicts, checks bool
		write             func(string) error
		refused           bool
	}{
		{"review comments", true, false, false, addWorkflow, true},
		{"review comments and checks", true, false, true, addWorkflow, true},
		{"review comments and conflicts", true, true, false, resolveAndAddWorkflow, true},
		{"checks alone", false, false, true, addWorkflow, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := NewRepositories(t)
			o := guardOptions(t, r, test.threads, test.write)
			o.Conflicts, o.Checks = test.conflicts, test.checks
			_, err := Run(context.Background(), o)
			if !test.refused {
				if err != nil || !pushedHead(t, r) {
					t.Fatalf("err %v, pushed %v", err, pushedHead(t, r))
				}
				return
			}
			if reasonOf(err) != ReasonWorkflowChanged || !strings.Contains(err.Error(), ".github/workflows/ci.yml") {
				t.Fatalf("err %v (reason %q)", err, reasonOf(err))
			}
			if pushedHead(t, r) {
				t.Fatal("the workflow change was pushed")
			}
		})
	}
}

// A workflow the base branch changed, brought in by the merge, is not the
// fix's change.
func TestFixPushesAWorkflowChangeTheBaseBrought(t *testing.T) {
	r := NewRepositories(t)
	gitIn(t, r.Seed, "checkout", "-q", "main")
	if err := writeIn(r.Seed, ".github/workflows/ci.yml", "on: push\n"); err != nil {
		t.Fatal(err)
	}
	gitIn(t, r.Seed, "add", "-A")
	gitIn(t, r.Seed, "commit", "-q", "-m", "a workflow on main")
	gitIn(t, r.Seed, "push", "-q", "origin", "main")
	o := guardOptions(t, r, true, func(dir string) error { return writeIn(dir, "a.txt", "feature and main\n") })
	o.Conflicts = true
	if _, err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if !pushedHead(t, r) {
		t.Fatal("nothing was pushed")
	}
}

func TestCredentialPath(t *testing.T) {
	for path, want := range map[string]bool{
		"id_rsa": true, "a/.ssh/ID_ED25519": true, "id_rsa.pub": false, ".netrc": true,
		".aws/credentials": true, "x/.aws/credentials": true, "credentials": false,
		".config/gh/hosts.yml": true, "hosts.yml": false, "docs/netrc.md": false,
	} {
		if got := credentialPath(path); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
	if got := strings.Join(filterWorkflows([]string{"a.go", ".github/actions/x/action.yml", ".GitHub/Workflows/y.yml", ".github/CODEOWNERS"}), ","); got != ".github/actions/x/action.yml,.GitHub/Workflows/y.yml" {
		t.Fatalf("workflows %v", got)
	}
}

func filterWorkflows(paths []string) []string {
	var out []string
	for _, p := range paths {
		if workflow(p) {
			out = append(out, p)
		}
	}
	return out
}
