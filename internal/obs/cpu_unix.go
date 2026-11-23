//go:build unix

package obs

import (
	"syscall"
	"time"
)

// cpuSeconds is the user and system CPU time the process has used.
func cpuSeconds() (user, system float64, ok bool) {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0, 0, false
	}
	return timeval(ru.Utime), timeval(ru.Stime), true
}

func timeval(tv syscall.Timeval) float64 {
	return (time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond).Seconds()
}
