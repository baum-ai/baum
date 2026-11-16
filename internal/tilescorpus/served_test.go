package tilescorpus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
)

// DigestsEnv, when set to 1 beside Env, makes TestServedDigests log one line
// per version: its position in the corpus and the SHA-256 of the answer the
// tile route serves for it with no composition (the encoded body and its
// ETag). Run it before and after a change to serving and diff the lines to
// show the answers did not change. The lines name no repository or pull
// request, only positions.
const DigestsEnv = "BAUM_TILES_CORPUS_DIGESTS"

func TestServedDigests(t *testing.T) {
	dir := os.Getenv(Env)
	if dir == "" || os.Getenv(DigestsEnv) != "1" {
		t.Skipf("%s and %s=1 are not set", Env, DigestsEnv)
	}
	for i, store := range findStores(t, dir) {
		for j, v := range readStore(t, i, store) {
			body, err := tileview.Encode(tileview.Build(v.document, nil, tiles.Options{}))
			if err != nil {
				t.Fatalf("store %d version %d: %v", i, j, err)
			}
			sum := sha256.Sum256(append(append([]byte{}, body.Body.Data...), body.Body.ETag...))
			fmt.Printf("DIGEST %04d-%04d %s\n", i, j, hex.EncodeToString(sum[:]))
		}
	}
}
