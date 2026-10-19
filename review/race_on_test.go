//go:build race

package review

// raceEnabled reports a build with the race detector, under which sync.Pool
// drops items at random, so allocation counts mean nothing.
const raceEnabled = true
