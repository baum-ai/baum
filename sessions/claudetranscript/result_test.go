package claudetranscript

import "testing"

func TestDecodeResultEvidenceReadsEveryShape(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		got := DecodeResultEvidence(nil)
		if got.Present {
			t.Fatal("a missing payload was reported as present")
		}
	})

	t.Run("a bare string", func(t *testing.T) {
		got := DecodeResultEvidence([]byte(`"User rejected tool use"`))
		if !got.Present || got.Text != "User rejected tool use" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("an array", func(t *testing.T) {
		got := DecodeResultEvidence([]byte(`[{"type":"text","text":"out"}]`))
		if !got.Present {
			t.Fatal("an array payload was reported as absent")
		}
	})

	t.Run("a command", func(t *testing.T) {
		got := DecodeResultEvidence([]byte(`{"stdout":"ok","stderr":"warn","interrupted":true,"exitCode":2}`))
		if got.Stdout != "ok" || got.Stderr != "warn" || !got.Interrupted {
			t.Fatalf("got %+v", got)
		}
		if got.ExitCode == nil || *got.ExitCode != 2 {
			t.Fatalf("exit code = %v, want 2", got.ExitCode)
		}
	})

	t.Run("an edit", func(t *testing.T) {
		got := DecodeResultEvidence([]byte(`{"filePath":"/a.go","structuredPatch":[{"oldStart":1,"oldLines":2,"newStart":1,"newLines":3,"lines":["-a","+b"]}]}`))
		if got.FilePath != "/a.go" {
			t.Fatalf("path = %q", got.FilePath)
		}
		if len(got.StructuredPatch) != 1 || got.StructuredPatch[0].NewLines != 3 {
			t.Fatalf("patch = %+v", got.StructuredPatch)
		}
	})

	t.Run("a read", func(t *testing.T) {
		got := DecodeResultEvidence([]byte(`{"type":"text","file":{"filePath":"/a.go","numLines":12,"startLine":5,"totalLines":400}}`))
		if got.FilePath != "/a.go" {
			t.Fatalf("path = %q, want the path from the nested file object", got.FilePath)
		}
		if got.StartLine == nil || *got.StartLine != 5 || got.NumLines == nil || *got.NumLines != 12 {
			t.Fatalf("span = %v..%v, want 5 and 12", got.StartLine, got.NumLines)
		}
	})

	t.Run("a spawn", func(t *testing.T) {
		got := DecodeResultEvidence([]byte(`{"agentId":"a1","status":"completed","totalTokens":42}`))
		if got.AgentID != "a1" || got.Status != "completed" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a field of an unexpected type", func(t *testing.T) {
		// One bad field must not cost the others: these payloads differ per tool
		// and are not versioned.
		got := DecodeResultEvidence([]byte(`{"stdout":["not","a","string"],"stderr":"kept","interrupted":"maybe"}`))
		if got.Stdout != "" {
			t.Fatalf("stdout = %q, want empty", got.Stdout)
		}
		if got.Stderr != "kept" {
			t.Fatalf("stderr = %q, want the readable field to survive", got.Stderr)
		}
		if got.Interrupted {
			t.Fatal("an unreadable interrupted flag was taken as true")
		}
	})
}

// WebFetch reports an HTTP status in `code`. Reading it as a process exit status
// turns three successful fetches in the corpus into failed tools.
func TestDecodeResultEvidenceIgnoresHTTPStatusCode(t *testing.T) {
	for _, payload := range []string{
		`{"code":200,"codeText":"OK","url":"https://example.test"}`,
		`{"code":403,"codeText":"Forbidden","url":"https://example.test"}`,
	} {
		got := DecodeResultEvidence([]byte(payload))
		if got.ExitCode != nil {
			t.Fatalf("%s: exit code = %d, want none", payload, *got.ExitCode)
		}
	}
}

func TestDecodeResultEvidenceReadsEveryExitCodeSpelling(t *testing.T) {
	for _, key := range exitCodeKeys {
		got := DecodeResultEvidence([]byte(`{"` + key + `":1}`))
		if got.ExitCode == nil || *got.ExitCode != 1 {
			t.Fatalf("%s: exit code = %v, want 1", key, got.ExitCode)
		}
	}
}
