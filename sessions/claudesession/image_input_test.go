package claudesession

import (
	"fmt"
	"testing"

	"github.com/baum-ai/baum/sessions/session"
)

func TestImageOnlyInputDistinguishesHumanAndHarnessOrigins(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
		want  session.InputOrigin
	}{
		{"ordinary", "", session.OriginHuman},
		{"typed", `"promptSource":"typed",`, session.OriginHuman},
		{"explicit_human", `"promptSource":"typed","origin":{"kind":"human"},`, session.OriginHuman},
		{"null_origin", `"origin":null,`, session.OriginHuman},
		{"meta", `"isMeta":true,`, session.OriginSystem},
		{"system", `"promptSource":"system",`, session.OriginSystem},
		{"peer", `"origin":{"kind":"peer"},`, session.OriginSystem},
		{"unknown_origin", `"origin":{"kind":"unknown"},`, session.OriginSystem},
		{"unknown_source", `"promptSource":"agent",`, session.OriginSystem},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := fmt.Sprintf(`{"type":"user","uuid":"image","sessionId":"root","timestamp":"2026-11-06T10:00:00Z",%s"message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}}`, tc.extra)
			graph, _ := buildClaudeLines(t, line)
			if len(graph.UserInputs) != 1 || graph.UserInputs[0].Origin != tc.want {
				t.Fatalf("inputs %+v", graph.UserInputs)
			}
			if tc.want == session.OriginHuman && graph.UserInputs[0].Kind != session.KindPrompt {
				t.Fatalf("human image-only kind %s", graph.UserInputs[0].Kind)
			}
		})
	}
}
