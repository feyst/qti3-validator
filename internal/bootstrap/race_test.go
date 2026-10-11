//go:build race

package bootstrap

// raceEnabled reports whether the tests run with the race detector, which
// changes what the heap holds.
const raceEnabled = true
