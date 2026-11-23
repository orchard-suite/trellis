package main

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// cgroupMemoryMax is where a container's memory limit is read (cgroup v2).
const cgroupMemoryMax = "/sys/fs/cgroup/memory.max"

// memoryShare is the part of the container's limit Go's heap may use; the
// rest is the stacks, the runtime, and what the kernel counts beside it.
const memoryShare = 0.85

// heapLimit is the soft limit for Go's heap: memoryShare of the container's
// memory limit, or 0 when there is none to fit, or when GOMEMLIMIT is set
// and so already chose one.
func heapLimit(gomemlimit string, read func(string) ([]byte, error)) int64 {
	if gomemlimit != "" {
		return 0
	}
	b, err := read(cgroupMemoryMax)
	if err != nil {
		return 0
	}
	limit, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || limit <= 0 {
		return 0
	}
	return int64(float64(limit) * memoryShare)
}

// fitMemory makes Go's collector work harder as the heap nears the
// container's limit, so a pass is slowed rather than killed. It says the
// limit it set, or 0.
func fitMemory() int64 {
	limit := heapLimit(os.Getenv("GOMEMLIMIT"), os.ReadFile)
	if limit > 0 {
		debug.SetMemoryLimit(limit)
	}
	return limit
}
