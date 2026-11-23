//go:build !unix

package obs

// cpuSeconds has no reading off Unix; the series is left out.
func cpuSeconds() (user, system float64, ok bool) { return 0, 0, false }
