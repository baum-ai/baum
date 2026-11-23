package agentcontext

import (
	"testing"

	"github.com/baum-ai/baum/internal/viewserver"
)

// The reader reads a document or the facts as large as the daemon serves one.
func TestDocumentReadBoundIsTheDaemons(t *testing.T) {
	if maxDocumentRead != viewserver.MaxDocumentBytes {
		t.Fatalf("the reader reads %d bytes of a document, the daemon serves %d", maxDocumentRead, viewserver.MaxDocumentBytes)
	}
}
