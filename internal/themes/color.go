package themes

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// RGBA is a colour in sRGB. Each channel is 0 to 1; A is the opacity.
type RGBA struct{ R, G, B, A float64 }

var errColor = errors.New("not a supported colour")

func parseHex(h string) (RGBA, error) {
	switch len(h) {
	case 3, 4:
		var b strings.Builder
		for _, r := range h {
			b.WriteRune(r)
			b.WriteRune(r)
		}
		h = b.String()
	case 6, 8:
	default:
		return RGBA{}, errColor
	}
	var c [4]float64
	c[3] = 1
	for i := 0; i < len(h)/2; i++ {
		v, err := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
		if err != nil {
			return RGBA{}, errColor
		}
		c[i] = float64(v) / 255
	}
	return RGBA{c[0], c[1], c[2], c[3]}, nil
}

func clamp(v float64) float64 { return math.Min(1, math.Max(0, v)) }

func hslToRGB(h, s, l float64) (r, g, b float64) {
	f := func(n float64) float64 {
		k := math.Mod(n+h/30, 12)
		a := s * math.Min(l, 1-l)
		return l - a*math.Max(-1, math.Min(math.Min(k-3, 9-k), 1))
	}
	return f(0), f(8), f(4)
}

// oklchToRGB clips each channel to the sRGB gamut, which approximates the
// gamut mapping CSS does for a colour outside it.
func oklchToRGB(l, c, h float64) (r, g, b float64) {
	hr := h * math.Pi / 180
	a, bb := c*math.Cos(hr), c*math.Sin(hr)
	l_ := l + 0.3963377774*a + 0.2158037573*bb
	m_ := l - 0.1055613458*a - 0.0638541728*bb
	s_ := l - 0.0894841775*a - 1.2914855480*bb
	l3, m3, s3 := l_*l_*l_, m_*m_*m_, s_*s_*s_
	lr := 4.0767416621*l3 - 3.3077115913*m3 + 0.2309699292*s3
	lg := -1.2684380046*l3 + 2.6097574011*m3 - 0.3413193965*s3
	lb := -0.0041960863*l3 - 0.7034186147*m3 + 1.7076147010*s3
	return clamp(encode(lr)), clamp(encode(lg)), clamp(encode(lb))
}

func encode(v float64) float64 {
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

func decode(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// Luminance is the WCAG 2 relative luminance of an opaque colour.
func (c RGBA) Luminance() float64 {
	return 0.2126*decode(c.R) + 0.7152*decode(c.G) + 0.0722*decode(c.B)
}

// Over composites c over an opaque background.
func (c RGBA) Over(bg RGBA) RGBA {
	return RGBA{
		c.R*c.A + bg.R*(1-c.A),
		c.G*c.A + bg.G*(1-c.A),
		c.B*c.A + bg.B*(1-c.A),
		1,
	}
}

// ratio is the WCAG 2 contrast ratio of two opaque colours.
func ratio(a, b RGBA) float64 {
	la, lb := a.Luminance(), b.Luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
