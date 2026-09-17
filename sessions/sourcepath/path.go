// Package sourcepath handles filesystem paths recorded on a machine whose OS
// may differ from the process reading them.
package sourcepath

import (
	"path"
	"strings"
)

// Style identifies the path syntax found in recorded data.
type Style uint8

const (
	StyleInvalid Style = iota
	StyleRelative
	StylePOSIX
	StyleWindowsDrive
	StyleWindowsUNC
	StyleWindowsDriveRelative
	StyleWindowsRooted
)

// Path is a lexically normalized recorded path. It does not inspect the local
// filesystem and therefore keeps the source machine's path semantics.
type Path struct {
	style        Style
	volume       string
	parts        []string
	text         string
	absolute     bool
	repoRelative bool
}

func Parse(raw string) Path {
	if raw == "" {
		return Path{}
	}
	value := strings.ReplaceAll(raw, `\`, "/")
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "//?/unc/"):
		return parseUNC(value[len("//?/UNC/"):])
	case strings.HasPrefix(lower, "//?/"):
		value = value[len("//?/"):]
		if !windowsDriveAbsolute(value) {
			return Path{}
		}
	case strings.HasPrefix(lower, "//./"):
		return Path{}
	}
	if len(value) >= 4 && value[0] == '/' && windowsDriveAbsolute(value[1:]) {
		value = value[1:]
	}
	if windowsDriveAbsolute(value) {
		return windowsDrivePath(value)
	}
	if strings.HasPrefix(value, "//") {
		return parseUNC(value[2:])
	}
	if strings.HasPrefix(raw, `\`) {
		parts := rootedParts(value)
		return Path{style: StyleWindowsRooted, parts: parts, text: windowsRootedText(parts)}
	}
	if strings.HasPrefix(value, "/") {
		parts := rootedParts(value)
		return Path{style: StylePOSIX, volume: "/", parts: parts, text: rootedText(parts), absolute: true}
	}
	if windowsDrivePrefix(value) {
		rest := path.Clean(value[2:])
		if rest == "." {
			rest = ""
		}
		return Path{
			style:  StyleWindowsDriveRelative,
			volume: strings.ToUpper(value[:1]) + ":",
			parts:  relativeParts(rest),
			text:   strings.ToUpper(value[:1]) + ":" + rest,
		}
	}
	cleaned := path.Clean(value)
	return Path{
		style: StyleRelative, parts: relativeParts(cleaned), text: cleaned,
		repoRelative: cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../"),
	}
}

// Normalize returns the canonical spelling of a recorded path.
func Normalize(value string) string { return Parse(value).String() }

// IsAbsolute reports whether value includes a complete POSIX, drive, or UNC root.
func IsAbsolute(value string) bool { return Parse(value).IsAbsolute() }

// IsRepositoryRelative reports whether value safely names a path below a repository root.
func IsRepositoryRelative(value string) bool { return Parse(value).IsRepositoryRelative() }

func (p Path) String() string { return p.text }

func (p Path) Style() Style { return p.style }

func (p Path) Valid() bool { return p.style != StyleInvalid }

func (p Path) IsAbsolute() bool { return p.absolute }

func (p Path) IsRepositoryRelative() bool { return p.repoRelative }

// Join resolves a repository-relative child below base using the recorded path syntax.
func Join(base, child string) string {
	basePath, childPath := Parse(base), Parse(child)
	if childPath.String() == "" || childPath.IsAbsolute() || !childPath.IsRepositoryRelative() {
		return childPath.String()
	}
	if basePath.String() == "" {
		return childPath.String()
	}
	return Parse(strings.TrimSuffix(basePath.String(), "/") + "/" + childPath.String()).String()
}

// Rel returns target relative to base when both use compatible absolute roots.
func Rel(base, target string) (string, bool) {
	basePath, targetPath := Parse(base), Parse(target)
	if !compatibleAbsolute(basePath, targetPath) || len(targetPath.parts) <= len(basePath.parts) {
		return "", false
	}
	for i := range basePath.parts {
		if !componentEqual(basePath.style, basePath.parts[i], targetPath.parts[i]) {
			return "", false
		}
	}
	return strings.Join(targetPath.parts[len(basePath.parts):], "/"), true
}

// HasPathSuffix reports whether an absolute path ends at a repository-relative path boundary.
func HasPathSuffix(value, suffix string) bool {
	valuePath, suffixPath := Parse(value), Parse(suffix)
	if !valuePath.IsAbsolute() || !suffixPath.IsRepositoryRelative() || len(suffixPath.parts) > len(valuePath.parts) {
		return false
	}
	offset := len(valuePath.parts) - len(suffixPath.parts)
	for i := range suffixPath.parts {
		if !componentEqual(valuePath.style, valuePath.parts[offset+i], suffixPath.parts[i]) {
			return false
		}
	}
	return true
}

func compatibleAbsolute(base, target Path) bool {
	if !base.IsAbsolute() || !target.IsAbsolute() || base.style != target.style {
		return false
	}
	if base.style == StyleWindowsDrive || base.style == StyleWindowsUNC {
		return strings.EqualFold(base.volume, target.volume)
	}
	return base.volume == target.volume
}

func componentEqual(style Style, left, right string) bool {
	if style == StyleWindowsDrive || style == StyleWindowsUNC {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func windowsDrivePath(value string) Path {
	volume := strings.ToUpper(value[:1]) + ":"
	parts := rootedParts(value[2:])
	text := volume + "/"
	if len(parts) > 0 {
		text += strings.Join(parts, "/")
	}
	return Path{style: StyleWindowsDrive, volume: volume, parts: parts, text: text, absolute: true}
}

func parseUNC(value string) Path {
	parts := strings.SplitN(value, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return Path{}
	}
	volume := "//" + parts[0] + "/" + parts[1]
	var tail []string
	if len(parts) == 3 {
		tail = rootedParts(parts[2])
	}
	text := volume
	if len(tail) > 0 {
		text += "/" + strings.Join(tail, "/")
	}
	return Path{style: StyleWindowsUNC, volume: volume, parts: tail, text: text, absolute: true}
}

func rootedParts(value string) []string {
	cleaned := path.Clean("/" + strings.TrimLeft(value, "/"))
	if cleaned == "/" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
}

func relativeParts(value string) []string {
	if value == "" || value == "." {
		return nil
	}
	return strings.Split(value, "/")
}

func rootedText(parts []string) string {
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

func windowsRootedText(parts []string) string {
	if len(parts) == 0 {
		return `\`
	}
	return `\` + strings.Join(parts, "/")
}

func windowsDrivePrefix(value string) bool {
	return len(value) >= 2 && asciiLetter(value[0]) && value[1] == ':'
}

func windowsDriveAbsolute(value string) bool {
	return len(value) >= 3 && windowsDrivePrefix(value) && value[2] == '/'
}

func asciiLetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}
