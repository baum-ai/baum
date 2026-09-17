package cursortranscript

import "testing"

func TestDecodeCodeBlockDiffRequiresBothArrays(t *testing.T) {
	diff, err := DecodeCodeBlockDiff([]byte(`{
		"newModelDiffWrtV0": [{
			"original": {"startLineNumber": 1, "endLineNumberExclusive": 2},
			"modified": []
		}],
		"originalModelDiffWrtV0": [],
		"futureField": true
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if diff.NewModelDiffWrtV0 == nil || diff.OriginalModelDiffWrtV0 == nil {
		t.Fatalf("empty arrays decoded as nil: %+v", diff)
	}
	if diff.NewModelDiffWrtV0[0].Modified == nil {
		t.Fatalf("empty modified array decoded as nil: %+v", diff)
	}
	if got := diff.NewModelDiffWrtV0[0].Original; got.StartLineNumber != 1 || got.EndLineNumberExclusive != 2 {
		t.Fatalf("original range = %+v, want [1,2)", got)
	}

	for _, raw := range []string{
		`{`,
		`{}`,
		`{"originalModelDiffWrtV0":[]}`,
		`{"newModelDiffWrtV0":[]}`,
		`{"newModelDiffWrtV0":[],"originalModelDiffWrtV0":null}`,
		`{"newModelDiffWrtV0":{},"originalModelDiffWrtV0":[]}`,
		`{"newModelDiffWrtV0":[{"original":{"startLineNumber":1,"endLineNumberExclusive":2}}],"originalModelDiffWrtV0":[]}`,
		`{"newModelDiffWrtV0":[{"original":{"startLineNumber":1,"endLineNumberExclusive":2},"modified":null}],"originalModelDiffWrtV0":[]}`,
		`{"newModelDiffWrtV0":[{"original":{"startLineNumber":1,"endLineNumberExclusive":2},"modified":[null]}],"originalModelDiffWrtV0":[]}`,
		`{"newModelDiffWrtV0":[{"original":{"endLineNumberExclusive":2},"modified":[]}],"originalModelDiffWrtV0":[]}`,
		`{"newModelDiffWrtV0":[{"original":{"startLineNumber":1},"modified":[]}],"originalModelDiffWrtV0":[]}`,
		`{"newModelDiffWrtV0":[{"original":{"startLineNumber":null,"endLineNumberExclusive":2},"modified":[]}],"originalModelDiffWrtV0":[]}`,
		`{"newModelDiffWrtV0":[{"original":{"startLineNumber":1,"endLineNumberExclusive":null},"modified":[]}],"originalModelDiffWrtV0":[]}`,
	} {
		if _, err := DecodeCodeBlockDiff([]byte(raw)); err == nil {
			t.Errorf("DecodeCodeBlockDiff(%s) succeeded", raw)
		}
	}
}
