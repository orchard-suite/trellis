package main

import (
	"errors"
	"testing"
)

func TestHeapLimitFitsTheContainer(t *testing.T) {
	file := func(content string) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return []byte(content), nil }
	}
	for _, c := range []struct {
		name, env string
		read      func(string) ([]byte, error)
		want      int64
	}{
		{"a 768Mi container", "", file("805306368\n"), 684510412},
		{"no limit", "", file("max\n"), 0},
		{"not in a cgroup", "", func(string) ([]byte, error) { return nil, errors.New("no file") }, 0},
		{"GOMEMLIMIT already chose", "600MiB", file("805306368\n"), 0},
	} {
		if got := heapLimit(c.env, c.read); got != c.want {
			t.Errorf("%s: heap limit %d, want %d", c.name, got, c.want)
		}
	}
}
