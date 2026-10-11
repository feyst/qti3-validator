//go:build !race

package app_test

// raceEnabled reports whether the tests run with the race detector, which
// makes validation several times slower.
const raceEnabled = false
