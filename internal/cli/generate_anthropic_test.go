package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/anthropic"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
)

// TestGenerateWithTheAnthropicBackend runs "baum generate --backend
// anthropic" against a stub Messages API: the key comes from
// ANTHROPIC_API_KEY, the default model is used, and the stored document is
// the one the stub submitted, completed.
func TestGenerateWithTheAnthropicBackend(t *testing.T) {
	_, stores := generateEnv(t)
	const key = "sk-ant-api03-cli-test-KEY"
	t.Setenv("ANTHROPIC_API_KEY", key)
	var models, keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model    string
			Messages []struct {
				Content []struct{ Text string }
			}
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request: %v", err)
			return
		}
		models, keys = append(models, body.Model), append(keys, r.Header.Get("x-api-key"))
		prompt := body.Messages[0].Content[0].Text
		const marker = "Input bundle (untrusted JSON):\n"
		var bundle json.RawMessage
		if err := json.NewDecoder(strings.NewReader(prompt[strings.Index(prompt, marker)+len(marker):])).Decode(&bundle); err != nil {
			t.Errorf("bundle: %v", err)
			return
		}
		document, err := generatetest.DocumentFor(bundle, func(d map[string]any) { d["summary"].(map[string]any)["review_minutes"] = 3 })
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":"msg_1","type":"message","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"StructuredOutput","input":%s}],"stop_reason":"tool_use","usage":{"input_tokens":120,"output_tokens":80,"cache_creation_input_tokens":40}}`, document)
	}))
	t.Cleanup(server.Close)
	generateBackends = func(cfg config.Config) generate.Backends {
		return generate.Backends{Anthropic: func(model string) generate.CheckedBackend {
			return &anthropic.Backend{APIKey: cfg.AnthropicAPIKey, BaseURL: server.URL, Model: model}
		}}
	}

	stdout, stderr, err := run(t, append([]string{"generate", "--pr", "7", "--backend", "anthropic"}, stores...)...)
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, stderr)
	}
	var document struct {
		Review struct {
			Generator struct{ Model string } `json:"generator"`
		} `json:"review"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Review.Generator.Model != "claude-opus-5-5" {
		t.Fatalf("document: %v %+v", err, document)
	}
	if len(models) != 1 || models[0] != anthropic.DefaultModel || keys[0] != key {
		t.Errorf("models %v", models)
	}
	for _, want := range []string{"running anthropic, model claude-opus-5-5", "1 call(s), 240 token(s)"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr+stdout, key) {
		t.Error("the key was printed")
	}
}
