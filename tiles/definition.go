package tiles

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	return digestOf(value)
}

// digestOf returns the digest of a decoded definition, leaving it as it
// was. Numbers may be json.Number or float64.
func digestOf(value any) (string, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("tiles: the definition is not an object")
	}
	without := make(map[string]any, len(object))
	for name, member := range object {
		if name != "digest" {
			without[name] = member
		}
	}
	var b bytes.Buffer
	if err := canonical(&b, without); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// The errors of decoding. They are fixed text: a problem built from one
// never repeats what it decoded.
var (
	errNotJSON       = errors.New("tiles: not valid JSON")
	errNotUTF8       = errors.New("tiles: not valid UTF-8")
	errRepeated      = errors.New("tiles: a member name is repeated in one object")
	errLoneSurrogate = errors.New("tiles: a string holds an escaped lone surrogate")
	errBadNumber     = errors.New("tiles: a number does not fit a double")
)

// decodeCanonical decodes JSON with json.Number numbers after refusing what
// encoding/json would otherwise accept silently: invalid UTF-8, escaped lone
// surrogates and repeated member names.
func decodeCanonical(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errNotUTF8
	}
	if err := checkEscapesAndMembers(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errNotJSON
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errNotJSON
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
			name := len(stack) > 0 && stack[len(stack)-1].object && stack[len(stack)-1].expect
			end, text, err := scanString(raw, i, name)
			if err != nil {
				return err
			}
			if name {
				n := len(stack)
				if stack[n-1].names[text] {
					return errRepeated
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
// at raw[start] and, when keep is set, its decoded text, refusing escaped
// lone surrogates.
func scanString(raw []byte, start int, keep bool) (int, string, error) {
	var b strings.Builder
	for i := start + 1; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"':
			return i, b.String(), nil
		case c != '\\':
			if keep {
				b.WriteByte(c)
			}
			continue
		}
		if i+1 >= len(raw) {
			break
		}
		i++
		if raw[i] != 'u' {
			if r, ok := simpleEscapes[raw[i]]; ok && keep {
				b.WriteByte(r)
			}
			continue
		}
		r, ok := hex4(raw, i+1)
		if !ok {
			return 0, "", errNotJSON
		}
		i += 4
		if utf16.IsSurrogate(r) {
			if r >= 0xdc00 || i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return 0, "", errLoneSurrogate
			}
			low, ok := hex4(raw, i+3)
			if !ok || low < 0xdc00 || low > 0xdfff {
				return 0, "", errLoneSurrogate
			}
			r = utf16.DecodeRune(r, low)
			i += 6
		}
		if keep {
			b.WriteRune(r)
		}
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
			return errBadNumber
		}
		b.WriteString(ecmaNumber(f))
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return errBadNumber
		}
		b.WriteString(ecmaNumber(v))
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
		return errNotJSON
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
// member names, without allocating. Code point order and UTF-16 order differ
// only between U+E000..U+FFFF and the supplementary planes, whose surrogates
// sort below U+E000.
func compareUTF16(a, b string) int {
	for a != "" && b != "" {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		a, b = a[na:], b[nb:]
		if ra == rb {
			continue
		}
		ha, la := utf16Units(ra)
		hb, lb := utf16Units(rb)
		if ha != hb {
			return cmp.Compare(ha, hb)
		}
		return cmp.Compare(la, lb)
	}
	return cmp.Compare(len(a), len(b))
}

// utf16Units returns a rune's first UTF-16 code unit and its second, or
// zero.
func utf16Units(r rune) (rune, rune) {
	if r < 0x10000 {
		return r, 0
	}
	high, low := utf16.EncodeRune(r)
	return high, low
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
