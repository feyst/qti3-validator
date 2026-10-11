//go:build race

package xpath

// slowdown is how much slower the race detector makes the code; the time
// limits of the scale tests are multiplied by it.
const slowdown = 10
