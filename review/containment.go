package review

// TextContainment is the overlap rule the validator uses for repeated
// points: the share of the shorter text's distinct words (case and stop
// words aside) that the longer text also has, or 0 when the shorter has
// fewer than six distinct words. Tile duplication checks use it too.
func TextContainment(a, b string) float64 { return textContainment(a, b) }

// NearDuplicate reports whether two texts state the same point by that rule:
// a containment of 0.8 or more.
func NearDuplicate(a, b string) bool { return textContainment(a, b) >= nearDuplicateThreshold }
