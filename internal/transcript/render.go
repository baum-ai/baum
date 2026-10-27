package transcript

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"github.com/baum-ai/baum/redact"
	"github.com/baum-ai/baum/sessions/session"
)

// Renderer writes a session's transcript as the local servers serve it: the
// review page's linked sessions (baum internal view and the app) and the
// app's live sessions both answer with what Render returns, so the two cannot
// differ in what they mask. It is safe for concurrent use.
type Renderer struct {
	homes []homeForm
	// engine redacts transcripts; it is compiled on the first one rendered.
	once      sync.Once
	engine    *redact.Engine
	engineErr error
}

// NewRenderer returns a renderer that writes home as ~; an empty home is
// UserHome's.
func NewRenderer(home string) *Renderer {
	if home == "" {
		home = UserHome()
	}
	return &Renderer{homes: homeForms(home)}
}

// Render shapes and bounds the graph first (DefaultLimits), so nothing past
// the limits is ever redacted or held, then redacts what is left: each text
// as it is kept, before it is cut, and the encoded transcript as a whole,
// every string and key of it, with the engine "baum internal sessions --dump"
// uses. The home directory is written as ~ last.
func (r *Renderer) Render(ctx context.Context, graph *session.Graph, meta Meta) ([]byte, error) {
	r.once.Do(func() { r.engine, r.engineErr = redact.New() })
	if r.engineErr != nil {
		return nil, r.engineErr
	}
	engine := r.engine
	shaped, err := Build(graph, meta, DefaultLimits, func(text string) (string, error) {
		masked, _, err := engine.RedactContext(ctx, text)
		return masked, err
	})
	if err != nil {
		return nil, err
	}
	body, err := Encode(shaped)
	if err != nil {
		return nil, fmt.Errorf("encoding the transcript: %w", err)
	}
	body, _, err = engine.RedactJSON(ctx, body)
	if err != nil {
		return nil, err
	}
	return replaceHome(body, r.homes), nil
}

// UserHome is the home directory: $HOME (or its platform's equivalent), else
// the account's, else none.
func UserHome() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if account, err := user.Current(); err == nil {
		return account.HomeDir
	}
	return ""
}

// homeForm is one way a JSON text can spell the home directory.
type homeForm struct {
	text []byte
	// fold matches regardless of case, as Windows paths do.
	fold bool
}

// homeForms lists the spellings of home to replace: as given (trailing
// separators dropped) and as its symbolic links resolve, each as is and
// JSON-escaped; for a Windows path also with forward slashes, matched
// regardless of case. A home of one character or none (the root) yields
// nothing.
func homeForms(home string) []homeForm {
	var paths []string
	add := func(path string) {
		path = strings.TrimRight(path, `/\`)
		if len(path) >= 2 && !strings.HasSuffix(path, ":") {
			for _, seen := range paths {
				if seen == path {
					return
				}
			}
			paths = append(paths, path)
		}
	}
	if home == "" {
		return nil
	}
	add(filepath.Clean(home))
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		add(resolved)
	}
	var forms []homeForm
	for _, path := range paths {
		windows := strings.Contains(path, `\`) || len(path) >= 2 && path[1] == ':'
		spellings := []string{path}
		if windows {
			spellings = append(spellings, strings.ReplaceAll(path, `\`, "/"))
		}
		for _, spelling := range spellings {
			forms = append(forms, homeForm{text: []byte(spelling), fold: windows})
			if escaped, err := json.Marshal(spelling); err == nil {
				if inner := escaped[1 : len(escaped)-1]; string(inner) != spelling {
					forms = append(forms, homeForm{text: inner, fold: windows})
				}
			}
		}
	}
	return forms
}

// replaceHome writes ~ for the home directory wherever a JSON text names it
// as a path of its own: after the start of the text, a quote, white space
// (an escaped newline or tab included), a separator or one of = : ( [ { , ;
// | < >, and not running on into a longer name. /data/home/user/src keeps
// its name, and so does a longer user's home beside this one.
func replaceHome(data []byte, forms []homeForm) []byte {
	for _, form := range forms {
		data = replaceWhole(data, form)
	}
	return data
}

func replaceWhole(data []byte, form homeForm) []byte {
	hay, needle := data, form.text
	if form.fold {
		hay, needle = bytes.ToLower(data), bytes.ToLower(form.text)
	}
	var out []byte
	last := 0
	for from := 0; ; {
		i := bytes.Index(hay[from:], needle)
		if i < 0 {
			break
		}
		at, end := from+i, from+i+len(needle)
		from = at + 1
		if !startsPath(data, at) || end < len(data) && continuesName(data[end]) {
			continue
		}
		out = append(out, data[last:at]...)
		out = append(out, '~')
		last, from = end, end
	}
	if out == nil {
		return data
	}
	return append(out, data[last:]...)
}

func startsPath(data []byte, at int) bool {
	if at == 0 || strings.IndexByte("\"'` \t\r\n=:([{,;|<>/", data[at-1]) >= 0 {
		return true
	}
	// An escaped newline, tab or carriage return in the JSON text.
	return at >= 2 && data[at-2] == '\\' && strings.IndexByte("ntr", data[at-1]) >= 0
}

func continuesName(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.'
}
