package tiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// TypeDefinition is the definition a custom tile embeds: a component, the
// schema of the data each review fills in, the instructions for filling it
// and when to use it. It has the shape of the tile schema's
// $defs/tileTypeDefinition.
type TypeDefinition struct {
	// Name is the type's name; it is never custom or a built-in type's.
	Name    string `json:"name"`
	Title   string `json:"title"`
	UseWhen string `json:"use_when"`
	Fill    string `json:"fill"`
	// DataSchema is in the restricted dialect CheckDataSchema checks.
	DataSchema json.RawMessage `json:"data_schema"`
	// Kit is the major version of the tile kit the component was written
	// for (KitSupported).
	Kit int `json:"kit"`
	// Sizes are the layout sizes the glance was made for, in order of
	// preference.
	Sizes  []Size     `json:"sizes"`
	Source TypeSource `json:"source"`
	// Digest is written by the host (DefinitionDigest).
	Digest string `json:"digest,omitempty"`
}

// TypeSource is a custom tile's component.
type TypeSource struct {
	Format string `json:"format"`
	HTML   string `json:"html"`
}

// KitLatest is the newest major version of the tile kit this version of
// baum serves. Every major from 1 to KitLatest is kept: a definition names
// the major it was written for, and a major only ever gains additions.
const KitLatest = 1

// KitSupported reports whether this version of baum serves the kit major a
// definition names.
func KitSupported(major int) bool {
	return major >= 1 && major <= KitLatest
}

// The limits of a definition and of a custom tile's data.
const (
	// MaxDefinitionBytes bounds an encoded definition before it is decoded.
	// The component may be 262144 characters, and an encoder may escape
	// every markup character in it.
	MaxDefinitionBytes = 2 << 20
	// maxDataSchemaBytes bounds a data schema, encoded without whitespace.
	maxDataSchemaBytes = 32 << 10
	// MaxDataBytes bounds a custom tile's data, encoded without whitespace.
	MaxDataBytes = 64 << 10
	// MaxCitations bounds the citations of one custom tile's data.
	MaxCitations = 256
)

// DefinitionDigest returns a definition's digest: "sha256:" and the
// lowercase hex SHA-256 of the definition without its "digest" member,
// canonicalised by RFC 8785 (JSON Canonicalization Scheme):
//
//   - no whitespace;
//   - object members sorted by their names' UTF-16 code units;
//   - numbers written as ECMAScript writes a double (1.0 and 1e0 are 1);
//   - strings escaped only where JSON requires it: '"', '\\', and control
//     characters as \b, \t, \n, \f, \r or a lowercase \u00XX; everything
//     else, U+2028 and U+2029 included, written as UTF-8;
//   - no Unicode normalisation.
//
// A definition that is not valid UTF-8, holds an escaped lone surrogate, a
// number a double cannot hold or a repeated member name has no digest, so
// every definition with a digest has exactly one canonical form.
func DefinitionDigest(definition json.RawMessage) (string, error) {
	if len(definition) > MaxDefinitionBytes {
		return "", fmt.Errorf("tiles: the definition is larger than %d bytes", MaxDefinitionBytes)
	}
	value, err := decodeCanonical(definition)
	if err != nil {
		return "", err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("tiles: the definition is not an object")
	}
	delete(object, "digest")
	var b bytes.Buffer
	if err := canonical(&b, object); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// decodeCanonical decodes JSON with json.Number numbers after refusing what
// encoding/json would otherwise accept silently: invalid UTF-8, escaped lone
// surrogates and repeated member names.
func decodeCanonical(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("tiles: not valid UTF-8")
	}
	if err := checkEscapesAndMembers(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("tiles: not valid JSON: %w", err)
	}
	if decoder.More() {
		return nil, errors.New("tiles: not valid JSON: data after the value")
	}
	return value, nil
}

// checkEscapesAndMembers scans valid-or-invalid JSON for escaped lone
// surrogates and repeated member names. It leaves syntax errors to the
// decoder.
func checkEscapesAndMembers(raw []byte) error {
	type frame struct {
		object bool
		names  map[string]bool
		expect bool // the next string is a member name
	}
	var stack []frame
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; c {
		case '{':
			stack = append(stack, frame{object: true, names: map[string]bool{}, expect: true})
		case '[':
			stack = append(stack, frame{})
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case ',':
			if n := len(stack); n > 0 && stack[n-1].object {
				stack[n-1].expect = true
			}
		case '"':
			end, text, err := scanString(raw, i)
			if err != nil {
				return err
			}
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expect {
				if stack[n-1].names[text] {
					return fmt.Errorf("tiles: member %q is repeated", truncate(text, 40))
				}
				stack[n-1].names[text] = true
				stack[n-1].expect = false
			}
			i = end
		}
	}
	return nil
}

// scanString returns the index of the closing quote of the string starting
// at raw[start] and its decoded text, refusing escaped lone surrogates.
func scanString(raw []byte, start int) (int, string, error) {
	var b strings.Builder
	for i := start + 1; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"':
			return i, b.String(), nil
		case c != '\\':
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(raw) {
			break
		}
		i++
		if raw[i] != 'u' {
			if r, ok := simpleEscapes[raw[i]]; ok {
				b.WriteByte(r)
			}
			continue
		}
		r, ok := hex4(raw, i+1)
		if !ok {
			return 0, "", errors.New("tiles: not valid JSON: a bad \\u escape")
		}
		i += 4
		if utf16.IsSurrogate(r) {
			if r >= 0xdc00 || i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return 0, "", errors.New("tiles: a string holds an escaped lone surrogate")
			}
			low, ok := hex4(raw, i+3)
			if !ok || low < 0xdc00 || low > 0xdfff {
				return 0, "", errors.New("tiles: a string holds an escaped lone surrogate")
			}
			r = utf16.DecodeRune(r, low)
			i += 6
		}
		b.WriteRune(r)
	}
	return len(raw), b.String(), nil
}

var simpleEscapes = map[byte]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}

func hex4(raw []byte, at int) (rune, bool) {
	if at+4 > len(raw) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(raw[at:at+4]), 16, 32)
	return rune(n), err == nil
}

// canonical writes a decoded JSON value in its RFC 8785 form.
func canonical(b *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil || math.IsInf(f, 0) {
			return fmt.Errorf("tiles: the number %s does not fit a double", truncate(string(v), 40))
		}
		b.WriteString(ecmaNumber(f))
	case string:
		canonicalString(b, v)
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canonical(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		names := make([]string, 0, len(v))
		for name := range v {
			names = append(names, name)
		}
		slices.SortFunc(names, compareUTF16)
		b.WriteByte('{')
		for i, name := range names {
			if i > 0 {
				b.WriteByte(',')
			}
			canonicalString(b, name)
			b.WriteByte(':')
			if err := canonical(b, v[name]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("tiles: cannot canonicalise a %T", value)
	}
	return nil
}

func canonicalString(b *bytes.Buffer, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[r>>4])
				b.WriteByte(hexDigits[r&0xf])
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// compareUTF16 orders strings by their UTF-16 code units, as RFC 8785 sorts
// member names.
func compareUTF16(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	return slices.Compare(ua, ub)
}

// ecmaNumber writes a double as ECMAScript's Number.prototype.toString does,
// which RFC 8785 requires: the shortest digits that round-trip, in plain
// notation from 1e-6 up to 1e21 and in exponent notation outside it.
func ecmaNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	format := byte('e')
	if f >= 1e-6 && f < 1e21 {
		format = 'f'
	}
	s := strconv.FormatFloat(f, format, -1, 64)
	if e := strings.IndexByte(s, 'e'); e > 0 && s[e+2] == '0' {
		// Go writes "1e+09"; ECMAScript writes "1e+9".
		s = s[:e+2] + s[e+3:]
	}
	return sign + s
}
