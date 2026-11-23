package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/orchard-suite/trellis"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/obs"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/sources"
	"github.com/orchard-suite/trellis/internal/sources/repo"
	"github.com/orchard-suite/trellis/internal/sources/telemetry"
	"github.com/orchard-suite/trellis/internal/sources/telemetry/probe"
	"github.com/orchard-suite/trellis/internal/sources/waivers"
)

// waiverFile is where check looks for waivers in the app's folder when
// -waivers names none.
const waiverFile = "trellis.waivers.yaml"

// checkReport is what check prints with -json.
type checkReport struct {
	Runs     []sources.Run          `json:"sources"`
	Packs    []judge.PackVerdict    `json:"packs"`
	Findings []judge.Finding        `json:"findings"`
	Subjects []judge.SubjectVerdict `json:"subjects"`
	Waivers  []judge.WaiverView     `json:"waivers"`
	FailOn   estate.Severity        `json:"fail_on"`
	// Over counts the open findings at or over FailOn; Unchecked the
	// counted rules that had no data, which fail the check with
	// -unchecked fail.
	Over       int       `json:"over"`
	Unchecked  int       `json:"unchecked"`
	Breaking   int       `json:"breaking"`
	CheckedAt  time.Time `json:"checked_at"`
	Repository string    `json:"repository"`
}

func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	dir := fs.String("dir", ".", "the app's folder: the repository root, or one app's folder")
	packs := fs.String("packs", "", "the packs to judge, comma separated; empty is every pack")
	packsDir := fs.String("packs-dir", "", "the estate's packs folder, with its own packs and its tuning.yaml")
	base := fs.String("base", "", "the git ref to compare definitions with; the latest release tag unless set; none to skip")
	match := fs.String("match", "", "the release tags to look among, a glob such as myapp-v* in a repository of several packages")
	failOn := fs.String("fail-on", "high", "the least severe finding that fails the check: critical, high, medium, or low")
	unchecked := fs.String("unchecked", "warn", "what a counted rule with no data does: warn, or fail")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	decl := fs.String("declaration", "", "a telemetry declaration to probe, with the three stores")
	prom := fs.String("prometheus", os.Getenv("PROMETHEUS_URL"), "the Prometheus to read")
	loki := fs.String("loki", os.Getenv("LOKI_URL"), "the Loki to read")
	tempo := fs.String("tempo", os.Getenv("TEMPO_URL"), "the Tempo to read")
	window := fs.Duration("window", probe.DefaultOptions.Window, "how far back the telemetry probes look")
	waiverPath := fs.String("waivers", "", "a waiver file; "+waiverFile+" in -dir when it is there")
	if err := fs.Parse(args); err != nil {
		return err
	}
	floor := estate.Severity(*failOn)
	if !floor.Valid() {
		return fmt.Errorf("-fail-on %q is not critical, high, medium, or low", *failOn)
	}
	if *unchecked != "warn" && *unchecked != "fail" {
		return fmt.Errorf("-unchecked %q is not warn or fail", *unchecked)
	}
	var on []string
	for _, p := range strings.Split(*packs, ",") {
		if p = strings.TrimSpace(p); p != "" {
			on = append(on, p)
		}
	}
	set, err := pack.Load(trellis.Packs(), *packsDir, on)
	if err != nil {
		return err
	}
	log := obs.NewLogger(io.Discard, meter.Service, version, obs.ParseLevel("info"))
	srcs := []sources.Named{{Name: "repo", Type: "repo", Source: &repo.Source{Dir: *dir, Base: *base, Match: *match, Log: log}}}
	if *waiverPath == "" {
		if _, err := os.Stat(filepath.Join(*dir, waiverFile)); err == nil {
			*waiverPath = filepath.Join(*dir, waiverFile)
		}
	}
	if *waiverPath != "" {
		srcs = append(srcs, sources.Named{Name: "waivers", Type: "waivers", Source: &waivers.Source{Path: *waiverPath}})
	}
	if *decl != "" {
		if *prom == "" || *loki == "" || *tempo == "" {
			return fmt.Errorf("-declaration needs -prometheus, -loki, and -tempo")
		}
		srcs = append(srcs, sources.Named{Name: "telemetry", Type: "telemetry", Source: telemetry.New(*decl, telemetry.Stores(*prom, *loki, *tempo), probe.Options{Window: *window}, "", log)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	e := estate.New(time.Now().UTC())
	rep := checkReport{FailOn: floor, CheckedAt: e.Now}
	for _, s := range srcs {
		rep.Runs = append(rep.Runs, sources.Collect(ctx, s, e))
	}
	ev := judge.Evaluate(e, set, judge.Options{Stage: pack.Check, Params: judge.Merge(set)})
	rep.Packs, rep.Findings, rep.Subjects, rep.Waivers = ev.Packs, ev.Findings, ev.Subjects, ev.Waivers
	for _, wv := range ev.Waivers {
		if wv.Status == judge.WaiverRejected {
			fmt.Fprintf(os.Stderr, "trellis: waiver %s ignored: %s\n", wv.Key(), wv.Problem)
		}
	}
	if repos := e.Of("repository"); len(repos) > 0 {
		rep.Repository = repos[0].Key
	}
	for _, f := range ev.Open() {
		if f.Severity.AtLeast(floor) {
			rep.Over++
		}
		if f.Rule == "api.breaking" {
			rep.Breaking++
		}
	}
	rep.Unchecked = ev.Estate.Unchecked
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		printCheck(os.Stdout, rep)
	}
	for _, r := range rep.Runs {
		if r.Outcome == sources.Failed {
			return fmt.Errorf("source %s failed: %s", r.Source, r.Error)
		}
	}
	if rep.Over > 0 {
		return &failure{n: rep.Over, floor: string(floor)}
	}
	if *unchecked == "fail" && rep.Unchecked > 0 {
		return fmt.Errorf("%d rules had no data, and -unchecked fail counts that as a failure", rep.Unchecked)
	}
	return nil
}

func printCheck(w io.Writer, rep checkReport) {
	fmt.Fprintf(w, "%s\n", rep.Repository)
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	open := 0
	for _, sv := range rep.Subjects {
		fmt.Fprintf(tw, "\n%s %s\t%s\n", sv.Kind, sv.Key, strings.ToUpper(string(sv.Level)))
		for _, f := range sv.Findings {
			switch f.State {
			case judge.Open:
				open++
				mark := "warn"
				if f.Severity.AtLeast(rep.FailOn) {
					mark = "FAIL"
				}
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", mark, f.Severity, f.Rule, f.Detail)
			case judge.Waived:
				fmt.Fprintf(tw, "  waived\t%s\t%s\t%s (%s, until %s)\n", f.Severity, f.Rule, f.Detail, f.Waiver.Reason, f.Waiver.Ends().Format(time.DateOnly))
			case judge.Announced:
				fmt.Fprintf(tw, "  soon\t%s\t%s\t%s (counts from %s)\n", f.Severity, f.Rule, f.Detail, f.CountsFrom.Format(time.DateOnly))
			case judge.Draft:
				fmt.Fprintf(tw, "  draft\t%s\t%s\t%s\n", f.Severity, f.Rule, f.Detail)
			}
		}
		for _, u := range sv.Unchecked {
			fmt.Fprintf(tw, "  -\t\t%s\t%s\n", u.Rule, u.Reason)
		}
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%d findings, %d at %s or worse, %d rules with no data\n", open, rep.Over, rep.FailOn, rep.Unchecked)
}
