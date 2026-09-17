package cursortranscript

import (
	"strings"
	"testing"
)

func TestDecodePortableClassifiesMessagesAndTurnEnds(t *testing.T) {
	input := strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>go</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"ok"},{"type":"tool_use","name":"Read","input":{"path":"main.go"}}]}}`,
		`{"type":"turn_ended","status":"error","error":"user aborted"}`,
	}, "\n")
	records, err := DecodePortable(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].Kind != PortableUser || records[1].Kind != PortableAssistant || records[2].Kind != PortableTurnEnd {
		t.Fatalf("records = %+v", records)
	}
	if len(records[1].Blocks) != 2 || records[1].Blocks[1].Tool == nil || records[1].Blocks[1].Tool.Name != "Read" {
		t.Fatalf("assistant blocks = %+v", records[1].Blocks)
	}
}

func TestDecodePortableRejectsUnknownShape(t *testing.T) {
	_, err := DecodePortable(strings.NewReader(`{"role":"user","message":{"content":[]},"id":"unexpected"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown message shape") {
		t.Fatalf("DecodePortable error = %v", err)
	}
}
