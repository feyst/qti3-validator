//go:build !race

package httpapi

// raceEnabled reports whether the tests run with the race detector, which
// changes what the heap holds.
const raceEnabled = false
