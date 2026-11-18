package themes

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/baum-ai/baum/tiles/kit"
)

var (
	recipesOnce sync.Once
	lightDecls  map[string]string
	darkDecls   map[string]string
)

func recipes() (light, dark map[string]string) {
	recipesOnce.Do(func() {
		lightDecls, darkDecls = map[string]string{}, map[string]string{}
		css := stripComments(kit.PaletteCSS())
		// Walk rules at any depth: a selector is the text before a "{" and a
		// rule's declarations are the text up to its "}".
		for {
			open := strings.IndexByte(css, '{')
			if open < 0 {
				return
			}
			sel := strings.TrimSpace(css[:open])
			if i := strings.LastIndexAny(sel, "};"); i >= 0 {
				sel = strings.TrimSpace(sel[i+1:])
			}
			rest := css[open+1:]
			next, closing := strings.IndexByte(rest, '{'), strings.IndexByte(rest, '}')
			if closing < 0 {
				return
			}
			if next >= 0 && next < closing {
				// A group rule such as @layer: its body holds the rules.
				css = rest
				continue
			}
			var into map[string]string
			switch sel {
			case ":root":
				into = lightDecls
			case `:root[data-theme="dark"]`:
				into = darkDecls
			}
			if into != nil {
				for _, decl := range strings.Split(rest[:closing], ";") {
					name, value, ok := strings.Cut(decl, ":")
					name = strings.TrimSpace(name)
					if ok && strings.HasPrefix(name, "--") {
						into[name] = strings.TrimSpace(value)
					}
				}
			}
			css = rest[closing+1:]
		}
	})
	return lightDecls, darkDecls
}

func stripComments(s string) string {
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "*/")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+2:]
	}
}

// Resolve evaluates the palette's recipes for a base ("light" or "dark") with
// tokens overriding them, as the viewer's cascade does, and returns every
// token that comes out as a colour. Tokens that are not colours (shadows,
// radii) are left out. A token value that is not a colour is an error.
func Resolve(base string, tokens map[string]string) (map[string]RGBA, error) {
	light, dark := recipes()
	decls := map[string]string{}
	for k, v := range light {
		decls[k] = v
	}
	switch base {
	case "light":
	case "dark":
		for k, v := range dark {
			decls[k] = v
		}
	default:
		return nil, errors.New("base must be light or dark")
	}
	for k, v := range tokens {
		if _, err := ParseColor(v); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		decls[k] = v
	}
	ev := &evaluator{decls: decls, done: map[string]RGBA{}, busy: map[string]bool{}}
	out := map[string]RGBA{}
	names := make([]string, 0, len(decls))
	for k := range decls {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if c, err := ev.token(k); err == nil {
			out[k] = c
		}
	}
	return out, nil
}

type evaluator struct {
	decls map[string]string
	done  map[string]RGBA
	busy  map[string]bool
}

func (e *evaluator) token(name string) (RGBA, error) {
	if c, ok := e.done[name]; ok {
		return c, nil
	}
	v, ok := e.decls[name]
	if !ok {
		return RGBA{}, fmt.Errorf("%s is not defined", name)
	}
	if e.busy[name] {
		return RGBA{}, fmt.Errorf("%s refers to itself", name)
	}
	e.busy[name] = true
	defer delete(e.busy, name)
	c, err := e.value(v)
	if err != nil {
		return RGBA{}, err
	}
	e.done[name] = c
	return c, nil
}

func (e *evaluator) value(v string) (RGBA, error) {
	v = strings.TrimSpace(v)
	lower := strings.ToLower(v)
	switch {
	case lower == "transparent":
		return RGBA{}, nil
	case strings.HasPrefix(lower, "var(") && strings.HasSuffix(v, ")"):
		return e.token(strings.TrimSpace(v[4 : len(v)-1]))
	case strings.HasPrefix(lower, "color-mix(") && strings.HasSuffix(v, ")"):
		return e.mix(v[len("color-mix(") : len(v)-1])
	}
	return ParseColor(v)
}

// mix evaluates the arguments of color-mix(in srgb, A p%, B q%).
func (e *evaluator) mix(args string) (RGBA, error) {
	parts := splitTop(args)
	if len(parts) != 3 || strings.Join(strings.Fields(strings.ToLower(parts[0])), " ") != "in srgb" {
		return RGBA{}, errors.New("unsupported color-mix")
	}
	var c [2]RGBA
	var p [2]float64
	var set [2]bool
	for i := range c {
		colour, pct, has, err := splitPercent(parts[i+1])
		if err != nil {
			return RGBA{}, err
		}
		if c[i], err = e.value(colour); err != nil {
			return RGBA{}, err
		}
		p[i], set[i] = pct, has
	}
	switch {
	case !set[0] && !set[1]:
		p = [2]float64{50, 50}
	case !set[0]:
		p[0] = 100 - p[1]
	case !set[1]:
		p[1] = 100 - p[0]
	}
	sum := p[0] + p[1]
	if sum <= 0 {
		return RGBA{}, errors.New("color-mix percentages add up to zero")
	}
	w0, w1 := p[0]/sum, p[1]/sum
	// Interpolate premultiplied, as CSS Color 4 does; percentages that add up
	// to less than 100 scale the result's opacity.
	alpha := c[0].A*w0 + c[1].A*w1
	out := RGBA{A: alpha * min(sum/100, 1)}
	if alpha > 0 {
		out.R = (c[0].R*c[0].A*w0 + c[1].R*c[1].A*w1) / alpha
		out.G = (c[0].G*c[0].A*w0 + c[1].G*c[1].A*w1) / alpha
		out.B = (c[0].B*c[0].A*w0 + c[1].B*c[1].A*w1) / alpha
	}
	return out, nil
}

// splitTop splits at commas outside parentheses.
func splitTop(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(parts, strings.TrimSpace(s[start:]))
}

// splitPercent separates "<colour> [<n>%]" or "[<n>%] <colour>".
func splitPercent(s string) (colour string, pct float64, has bool, err error) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, ' '); i >= 0 && strings.HasSuffix(s, "%") && !strings.ContainsAny(s[i:], "()") {
		pct, err = parseFloat(strings.TrimSuffix(s[i+1:], "%"))
		return strings.TrimSpace(s[:i]), pct, true, err
	}
	if i := strings.IndexByte(s, ' '); i >= 0 && strings.HasSuffix(s[:i], "%") {
		pct, err = parseFloat(strings.TrimSuffix(s[:i], "%"))
		return strings.TrimSpace(s[i+1:]), pct, true, err
	}
	return s, 0, false, nil
}
