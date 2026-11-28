package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/orchard-suite/trellis"

	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/packtest"
)

// runTest holds an estate's packs to their fixtures before they are
// switched on: every rule needs a failing, a passing, and a no-data case.
func runTest(args []string) error {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	dir := fs.String("packs-dir", ".", "the folder of packs to test")
	fixtures := fs.String("fixtures", "", "the folder of fixture files, one per pack; testdata in -packs-dir unless set")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fixtures == "" {
		*fixtures = filepath.Join(*dir, "testdata")
	}
	// Every pack with the estate's tuning, as the board and the judge load
	// them; only the estate's own are held to its fixtures.
	board, err := pack.Load(trellis.Packs(), *dir, nil)
	if err != nil {
		return err
	}
	read, err := pack.ReadAll(trellis.Packs(), *dir)
	if err != nil {
		return err
	}
	var gates []string
	for id, p := range read {
		if p.Gate() && id != pack.BaseGate {
			gates = append(gates, id)
		}
	}
	sort.Strings(gates)
	gate, err := pack.LoadGate(trellis.Packs(), *dir, gates)
	if err != nil {
		return err
	}
	set := &pack.Set{Params: map[string]map[string]any{}}
	for _, side := range []*pack.Set{board, gate} {
		for id, v := range side.Params {
			set.Params[id] = v
		}
		for _, p := range side.Packs {
			if !strings.HasPrefix(p.Source, "built in:") {
				set.Packs = append(set.Packs, p)
			}
		}
	}
	fx, err := packtest.ReadDir(*fixtures)
	if err != nil {
		return err
	}
	problems := packtest.Run(set, fx)
	for _, p := range problems {
		fmt.Println(p)
	}
	rules := len(set.Rules())
	fmt.Printf("%d packs, %d rules, %d problems\n", len(set.Packs), rules, len(problems))
	if len(problems) > 0 {
		return &failure{n: len(problems), floor: "fixture"}
	}
	return nil
}
