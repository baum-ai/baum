package traceprocessing

import (
	"cmp"
	"strconv"
	"strings"
)

const unknownVersion = "unknown"

// Versions counts reported versions and their numeric-or-lexical bounds.
type Versions struct {
	Seen map[string]int `json:"seen"`
	Min  string         `json:"min"`
	Max  string         `json:"max"`
}

func NewVersions() Versions { return Versions{Seen: map[string]int{}} }

func (v *Versions) Add(version string) {
	if version == "" {
		version = unknownVersion
	}
	v.Seen[version]++
}

func (v *Versions) Finalize() {
	v.Min, v.Max = "", ""
	for version := range v.Seen {
		if version == unknownVersion {
			continue
		}
		if v.Min == "" || compareVersions(version, v.Min) < 0 {
			v.Min = version
		}
		if v.Max == "" || compareVersions(version, v.Max) > 0 {
			v.Max = version
		}
	}
	if v.Min == "" {
		v.Min, v.Max = unknownVersion, unknownVersion
	}
}

func compareVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(left), len(right)) {
		one, other := versionPart(left, i), versionPart(right, i)
		oneNum, oneErr := strconv.Atoi(one)
		otherNum, otherErr := strconv.Atoi(other)
		if oneErr == nil && otherErr == nil {
			if oneNum != otherNum {
				return cmp.Compare(oneNum, otherNum)
			}
		} else if one != other {
			return strings.Compare(one, other)
		}
	}
	return 0
}

func versionPart(parts []string, i int) string {
	if i < len(parts) {
		return parts[i]
	}
	return ""
}
