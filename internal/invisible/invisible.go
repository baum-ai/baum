// Package invisible reports characters that draw nothing yet change how
// text reads or what a model takes from it.
package invisible

import "unicode"

// Is reports whether r is a default-ignorable code point, which renders as
// nothing or changes how the text around it renders: format characters (Cf:
// bidirectional controls, zero-width characters, the soft hyphen, tags),
// variation selectors and the other default-ignorables (the combining
// grapheme joiner U+034F, the Hangul fillers U+115F, U+1160, U+3164 and
// U+FFA0, the Khmer inherent vowels U+17B4 and U+17B5). Go's tables have no
// Default_Ignorable_Code_Point property; these three are what it is made
// of, and the named ones are listed again so a table change cannot drop
// them.
func Is(r rune) bool {
	switch r {
	case 0x034F, 0x115F, 0x1160, 0x17B4, 0x17B5, 0x3164, 0xFFA0:
		return true
	}
	return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r)
}
