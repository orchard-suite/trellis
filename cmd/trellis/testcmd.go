package main

import (
	"flag"
	"fmt"
	"path/filepath"
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
	// Every pack with the estate's tuning, as the board loads them; only
	// the estate's own are held to its fixtures.
	all, err := pack.Load(trellis.Packs(), *dir, nil)
	if err != nil {
		return err
	}
	set := &pack.Set{Params: all.Params}
	for _, p := range all.Packs {
		if !strings.HasPrefix(p.Source, "built in:") {
			set.Packs = append(set.Packs, p)
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
