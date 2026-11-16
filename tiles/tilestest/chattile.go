package tilestest

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
)

// ChatTileName is the name of the tile made in chat ChatTile gives.
const ChatTileName = "chat_made"

// chatSealed is the starter under ChatTileName, sealed, made once.
type chatSealed struct {
	def     tiles.TypeDefinition
	encoded json.RawMessage
}

var chatOnce = sync.OnceValues(func() (chatSealed, error) {
	e, err := library.Load(kit.Starter())
	if err != nil {
		return chatSealed{}, err
	}
	def := e.Definition
	def.Name, def.Title = ChatTileName, "Made in chat"
	encoded, err := library.Seal(&def)
	return chatSealed{def: def, encoded: encoded}, err
})

func chatDefinition() (tiles.TypeDefinition, json.RawMessage, error) {
	c, err := chatOnce()
	return c.def, c.encoded, err
}

// ChatTile is a tile made in chat for a converted document, as a person's
// overlay adds it: the kit's starter under ChatTileName, its add and the
// tile the host holds for it, its data citing the document's first hunk.
// ok is false for a document without hunks.
func ChatTile(doc *tiles.Document) (add tiles.Added, held tiles.LibraryTile, ok bool) {
	diff := library.DiffOf(doc)
	if len(diff) == 0 {
		return tiles.Added{}, tiles.LibraryTile{}, false
	}
	def, encoded, err := chatDefinition()
	if err != nil {
		return tiles.Added{}, tiles.LibraryTile{}, false
	}
	data := fmt.Sprintf(`{"summary":"One finding.","items":[{"title":"A finding","severity":"low","text":null,"cites":[{"type":"hunk","id":%q}]}]}`, diff[0].ID)
	return tiles.Added{Kind: tiles.AddCustom, Name: ChatTileName, Digest: def.Digest, Title: def.Title},
		tiles.LibraryTile{Definition: encoded, Data: json.RawMessage(data)}, true
}
