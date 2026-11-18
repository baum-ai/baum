package themes

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// The grammar ParseColor reads is small on purpose, so a port in another
// language with the same regular expressions behaves the same:
//
//	hex      #rgb #rgba #rrggbb #rrggbbaa
//	legacy   rgb(r, g, b) rgba(r, g, b, a) hsl(h, s%, l%) hsla(h, s%, l%, a)
//	modern   rgb(r g b) rgb(r g b / a) hsl(h s% l%) hsl(h s% l% / a)
//	         oklch(l c h) oklch(l c h / a)
//
// A number is cssNumber; r, g, b, l, c and a may be numbers or percentages
// (legacy rgb takes all three channels as numbers or all as percentages); a
// hue is a number with an optional deg, grad, rad or turn unit. Whitespace is
// ASCII space, tab, line feed, carriage return and form feed only.
var (
	cssNumber = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)
	function  = regexp.MustCompile(`^(rgba?|hsla?|oklch)\(([^()]*)\)$`)
)

const asciiSpace = " \t\n\r\f"

func trimASCII(s string) string { return strings.Trim(s, asciiSpace) }

func fieldsASCII(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(asciiSpace, r) })
}

// ParseColor reads a hex colour (#rgb, #rgba, #rrggbb, #rrggbbaa) or an
// rgb(), rgba(), hsl(), hsla() or oklch() function and converts it to sRGB.
// Anything else, keywords, var() and url() included, is refused.
func ParseColor(s string) (RGBA, error) {
	s = strings.ToLower(trimASCII(s))
	if strings.HasPrefix(s, "#") {
		return parseHex(s[1:])
	}
	m := function.FindStringSubmatch(s)
	if m == nil {
		return RGBA{}, errColor
	}
	name, body := m[1], trimASCII(m[2])
	var fields []string
	var alpha string
	legacy := strings.Contains(body, ",")
	if legacy {
		if strings.Contains(body, "/") || name == "oklch" {
			return RGBA{}, errColor
		}
		for _, part := range strings.Split(body, ",") {
			part = trimASCII(part)
			if part == "" || len(fieldsASCII(part)) != 1 {
				return RGBA{}, errColor
			}
			fields = append(fields, part)
		}
		if len(fields) == 4 {
			fields, alpha = fields[:3], fields[3]
		}
	} else {
		colour := body
		if before, after, ok := strings.Cut(body, "/"); ok {
			colour = before
			if alpha = trimASCII(after); alpha == "" || len(fieldsASCII(alpha)) != 1 {
				return RGBA{}, errColor
			}
		}
		fields = fieldsASCII(colour)
	}
	if len(fields) != 3 {
		return RGBA{}, errColor
	}
	a := 1.0
	if alpha != "" {
		v, err := number(alpha, 1)
		if err != nil {
			return RGBA{}, err
		}
		a = clamp(v)
	}
	switch name {
	case "rgb", "rgba":
		var c [3]float64
		percents := 0
		for i, f := range fields {
			if strings.HasSuffix(f, "%") {
				percents++
			}
			v, err := number(f, 255)
			if err != nil {
				return RGBA{}, err
			}
			c[i] = clamp(v / 255)
		}
		if legacy && percents != 0 && percents != 3 {
			return RGBA{}, errColor
		}
		return RGBA{c[0], c[1], c[2], a}, nil
	case "hsl", "hsla":
		h, err := hue(fields[0])
		if err != nil {
			return RGBA{}, err
		}
		sat, err := percent(fields[1])
		if err != nil {
			return RGBA{}, err
		}
		light, err := percent(fields[2])
		if err != nil {
			return RGBA{}, err
		}
		r, g, b := hslToRGB(h, clamp(sat), clamp(light))
		return RGBA{r, g, b, a}, nil
	case "oklch":
		l, err := number(fields[0], 1)
		if err != nil {
			return RGBA{}, err
		}
		c, err := number(fields[1], 0.4)
		if err != nil {
			return RGBA{}, err
		}
		h, err := hue(fields[2])
		if err != nil {
			return RGBA{}, err
		}
		r, g, b := oklchToRGB(clamp(l), math.Max(c, 0), h)
		return RGBA{r, g, b, a}, nil
	}
	return RGBA{}, errColor
}

// parseFloat reads a CSS number: not Go's syntax (no hex, underscores, inf or
// nan) and a finite value.
func parseFloat(s string) (float64, error) {
	if !cssNumber.MatchString(s) {
		return 0, errColor
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errColor
	}
	return v, nil
}

func finite(v float64, err error) (float64, error) {
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errColor
	}
	return v, nil
}

// number reads a number or a percentage; a percentage is a share of full.
func number(s string, full float64) (float64, error) {
	if p, ok := strings.CutSuffix(s, "%"); ok {
		v, err := parseFloat(p)
		return finite(v/100*full, err)
	}
	return parseFloat(s)
}

func percent(s string) (float64, error) {
	p, ok := strings.CutSuffix(s, "%")
	if !ok {
		return 0, errColor
	}
	v, err := parseFloat(p)
	return finite(v/100, err)
}

func hue(s string) (float64, error) {
	scale := 1.0
	switch {
	case strings.HasSuffix(s, "deg"):
		s = strings.TrimSuffix(s, "deg")
	case strings.HasSuffix(s, "grad"):
		s, scale = strings.TrimSuffix(s, "grad"), 0.9
	case strings.HasSuffix(s, "turn"):
		s, scale = strings.TrimSuffix(s, "turn"), 360
	case strings.HasSuffix(s, "rad"):
		s, scale = strings.TrimSuffix(s, "rad"), 180/math.Pi
	}
	v, err := parseFloat(s)
	v, err = finite(v*scale, err)
	if err != nil {
		return 0, err
	}
	return math.Mod(math.Mod(v, 360)+360, 360), nil
}
