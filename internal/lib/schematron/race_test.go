//go:build race

package schematron

// slowdown is how much slower the race detector makes the code; the time
// limits of the scale tests are multiplied by it.
const slowdown = 10
