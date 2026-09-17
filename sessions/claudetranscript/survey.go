package claudetranscript

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/lastpersonlabs/reviewpal/sessions/traceprocessing"
)

type Shape struct {
	Versions   traceprocessing.Versions `json:"versions"`
	Content    map[string]int           `json:"content_shapes,omitempty"`
	UnreadKeys map[string]int           `json:"unread_keys,omitempty"`
}

func Survey(files []*File) Shape {
	shape := Shape{
		Versions:   traceprocessing.NewVersions(),
		Content:    map[string]int{},
		UnreadKeys: map[string]int{},
	}
	for _, file := range files {
		for _, line := range file.Lines {
			shape.Versions.Add(line.Version)

			if line.Message != nil {
				shape.Content[contentShape(line.Message.Content)]++
			}
			for _, key := range unreadKeys(line.Raw) {
				shape.UnreadKeys[key]++
			}
		}
	}
	shape.Versions.Finalize()
	return shape
}

func contentShape(content json.RawMessage) string {
	switch {
	case len(content) == 0:
		return "absent"
	case content[0] == '"':
		return "string"
	case content[0] == '[':
		return "array"
	default:
		return "other"
	}
}

// readKeys are the top-level fields rawLine decodes.
var readKeys = jsonFieldNames(reflect.TypeFor[rawLine]())

func jsonFieldNames(t reflect.Type) map[string]bool {
	out := make(map[string]bool, t.NumField())
	for field := range t.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

func unreadKeys(raw json.RawMessage) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	var out []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return out
		}
		name, ok := token.(string)
		if !ok {
			return out
		}
		if !readKeys[name] {
			out = append(out, name)
		}
		if err := skipValue(decoder); err != nil {
			return out
		}
	}
	return out
}

// skipValue consumes the next value, however deeply nested.
func skipValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if _, nested := token.(json.Delim); !nested {
		return nil
	}
	for decoder.More() {
		if err := skipValue(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
