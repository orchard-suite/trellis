package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/orchard-suite/trellis"
	"github.com/orchard-suite/trellis/internal/azure"
	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/config"
	"github.com/orchard-suite/trellis/internal/history"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/obs"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/relay"
	"github.com/orchard-suite/trellis/internal/server"
	"github.com/orchard-suite/trellis/internal/sinks/webhook"
	"github.com/orchard-suite/trellis/internal/sources"
	_ "github.com/orchard-suite/trellis/internal/sources/all"
	"github.com/orchard-suite/trellis/internal/sources/orrery"
)

func configPath(fs *flag.FlagSet) *string {
	return fs.String("config", env("TRELLIS_CONFIG", "/etc/trellis/config.yaml"), "the board's settings file")
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	path := configPath(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runBoard(*path, "", false)
}

func runOnce(args []string) error {
	fs := flag.NewFlagSet("once", flag.ContinueOnError)
	path := configPath(fs)
	drafts := fs.String("packs-dir", "", "a folder of packs to try on the estate, beside the configured ones")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runBoard(*path, *drafts, true)
}

// runBoard judges the estate: once, printing the pass, or on a schedule
// behind the board's two ports.
func runBoard(path, extraPacks string, once bool) error {
	log := obs.NewLogger(os.Stdout, meter.Service, version, obs.ParseLevel(os.Getenv("LOG_LEVEL")))
	if limit := fitMemory(); limit > 0 {
		log.Info("heap fitted to the container", "limit_bytes", limit)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	shutdown, err := obs.Setup(ctx, meter.Service, version)
	if err != nil {
		return err
	}
	fail := func(err error) error { return errors.Join(err, shutdown(context.Background())) }
	metrics, err := meter.New()
	if err != nil {
		return fail(err)
	}
	set, err := loadPacks(cfg.Packs, extraPacks)
	if err != nil {
		return fail(err)
	}
	opts, err := cfg.Options(judge.Merge(set))
	if err != nil {
		return fail(err)
	}
	srcs, err := buildSources(cfg, log)
	if err != nil {
		return fail(err)
	}

	var sinks []board.Sink
	var hub *orrery.Client
	if o := cfg.Sinks.Orrery; o != nil && !once {
		hub = orrery.NewClient(o.URL, o.Token, log)
		sink, err := orrery.NewSink(hub, cfg.PublicURL, trellis.Blueprints, o.Write, o.Scorecards, func() []string { return linked(srcs) }, metrics, log)
		if err != nil {
			return fail(err)
		}
		sinks = append(sinks, board.Sink{Name: "orrery", Push: sink.Push})
		log.Info("findings will be written to orrery", "url", o.URL)
	}
	if w := cfg.Sinks.Webhook; w != nil && !once {
		if w.URL == "" {
			return fail(errors.New("the webhook sink needs url"))
		}
		sinks = append(sinks, board.Sink{Name: "webhook", Push: webhook.New(w.URL, w.Token, cfg.Estate, metrics, log).Push})
	}
	b := board.New(srcs, set, opts, metrics, log, sinks...)
	for _, u := range board.Unwritten(set, board.Written(srcs)) {
		log.Warn("rule reads facts no source writes; it can only answer no data", "rule", u.Rule, "facts", strings.Join(u.Facts, ","))
	}
	var hist *history.Postgres
	if h := cfg.History; h != nil && !once {
		hist, err = openHistory(ctx, h)
		if err != nil {
			return fail(err)
		}
		defer hist.Close()
		b.Record(history.Recorder(hist, history.Window), hist.Opened)
		go keepPruned(ctx, hist, h.RetentionDays, log)
		log.Info("history is kept", "sign_in", h.SignIn, "retention_days", h.RetentionDays)
	}

	if once {
		printOnce(os.Stdout, b.Refresh(ctx))
		return shutdown(context.Background())
	}

	rel, err := relay.NewRelay(relay.Config{Service: meter.Service, Version: version, URL: cfg.BrowserTelemetry.URL, Key: cfg.BrowserTelemetry.Key}, log)
	if err != nil {
		return fail(err)
	}
	var past server.History
	if hist != nil {
		past = hist
	}
	srv, err := server.New(server.Config{PublicURL: cfg.PublicURL, Estate: cfg.Estate, SignIn: cfg.SignIn, Version: version}, b, rel, log, b.Check, past)
	if err != nil {
		return fail(err)
	}
	go b.Run(ctx, time.Duration(cfg.Refresh))

	listeners := []struct {
		addr    string
		handler http.Handler
	}{
		{cfg.Listen.Session, srv.Session()},
		{cfg.Listen.Health, srv.Health()},
		{cfg.Listen.Control, srv.Control()},
	}
	if cfg.Listen.Profile != "" {
		listeners = append(listeners, struct {
			addr    string
			handler http.Handler
		}{cfg.Listen.Profile, obs.Profiler()})
		log.Info("profiles are served", "listen", cfg.Listen.Profile)
	}
	errs := make(chan error, len(listeners))
	servers := make([]*http.Server, 0, len(listeners))
	for _, l := range listeners {
		hs := &http.Server{Addr: l.addr, Handler: l.handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		servers = append(servers, hs)
		go func() {
			if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}
	names := make([]string, 0, len(srcs))
	for _, s := range srcs {
		names = append(names, s.Name)
	}
	packs := make([]string, 0, len(set.Packs))
	for _, p := range set.Packs {
		packs = append(packs, p.ID+"@"+p.Version)
	}
	log.Info("trellis started", "estate", cfg.Estate, "public_url", cfg.PublicURL, "sources", strings.Join(names, ","), "packs", strings.Join(packs, ","), "refresh_interval", time.Duration(cfg.Refresh).String(), "control", cfg.Listen.Control)

	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	log.Info("trellis stopping")
	done, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, hs := range servers {
		_ = hs.Shutdown(done)
	}
	return errors.Join(err, shutdown(done))
}

// openHistory connects to the history database, signing in as the pod's
// workload identity when it says azure.
func openHistory(ctx context.Context, h *config.History) (*history.Postgres, error) {
	var token history.TokenFunc
	if h.SignIn == "azure" {
		id, err := azure.IdentityFromEnv()
		if err != nil {
			return nil, fmt.Errorf("history: %w", err)
		}
		token = func(ctx context.Context) (string, error) { return azure.SignIn(ctx, id, azure.PostgresScope) }
	}
	return history.Open(ctx, h.URL, token)
}

// keepPruned drops history older than the retention once a day.
func keepPruned(ctx context.Context, h *history.Postgres, days int, log *slog.Logger) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		if err := h.Prune(ctx, cutoff); err != nil {
			log.ErrorContext(ctx, "history prune failed", "error", err)
		} else {
			log.InfoContext(ctx, "history pruned", "older_than", cutoff.Format(time.DateOnly))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// loadPacks reads the built-in packs and the estate's with its tuning,
// and, for a trial, adds a folder more, whose packs are switched on too.
func loadPacks(p config.Packs, extra string) (*pack.Set, error) {
	set, err := pack.Load(trellis.Packs(), p.Dir, p.On)
	if err != nil {
		return nil, err
	}
	if extra != "" {
		trial, err := pack.Load(os.DirFS(extra), "", nil)
		if err != nil {
			return nil, err
		}
		set.Add(trial)
	}
	return set, nil
}

// linked is every list a catalog source maps that its hub has now, which a
// finding may link to.
func linked(srcs []sources.Named) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range srcs {
		l, ok := s.Source.(interface{ Linked() []string })
		if !ok {
			continue
		}
		for _, bp := range l.Linked() {
			if !seen[bp] {
				seen[bp] = true
				out = append(out, bp)
			}
		}
	}
	sort.Strings(out)
	return out
}

// buildSources makes each configured source, in order, through the
// registry. Two sources may not share a name: facts are stamped with it.
func buildSources(cfg *config.Config, log *slog.Logger) ([]sources.Named, error) {
	var out []sources.Named
	seen := map[string]bool{}
	for i := range cfg.Sources {
		n, err := sources.Build(&cfg.Sources[i], sources.Env{Log: log, Dir: cfg.Dir})
		if err != nil {
			return nil, err
		}
		if seen[n.Name] {
			return nil, fmt.Errorf("two sources are named %s; give one a name", n.Name)
		}
		seen[n.Name] = true
		out = append(out, n)
	}
	return out, nil
}

// printOnce writes a one-pass summary.
func printOnce(w io.Writer, snap *board.Snapshot) {
	ev := snap.Evaluation
	share := "n/a"
	if c := ev.Estate.Conformant; c != nil {
		share = fmt.Sprintf("%.1f%%", *c)
	}
	byState := map[judge.State]int{}
	for _, f := range ev.Findings {
		byState[f.State]++
	}
	fmt.Fprintf(w, "estate: %s, %s conformant (%d open, %d overdue, %d waived, %d announced, %d draft, %d unchecked, %d subjects, %s)\n", ev.Estate.Level, share, byState[judge.Open], ev.Estate.Overdue, byState[judge.Waived], byState[judge.Announced], byState[judge.Draft], ev.Estate.Unchecked, len(ev.Subjects), snap.Took.Round(time.Millisecond))
	for _, r := range snap.Runs {
		fmt.Fprintf(w, "source %-12s %-6s %8s missed=%d carried=%d %s\n", r.Source, r.Outcome, r.Duration.Round(time.Millisecond), r.Missed, r.Carried, r.Error)
		for _, g := range r.Gaps {
			fmt.Fprintf(w, "source %-12s unread %s.%s: %s\n", r.Source, g.Kind, g.Fact, g.Reason)
		}
	}
	for _, p := range ev.Packs {
		fmt.Fprintf(w, "pack %-11s %-6s %-5s subjects=%d red=%d amber=%d green=%d grey=%d findings=%d overdue=%d waived=%d unchecked=%d\n", p.Pack, p.State, p.Level, p.Subjects, p.Red, p.Amber, p.Green, p.Grey, p.Findings, p.Overdue, p.Waived, p.Unchecked)
	}
	for _, k := range ev.Kinds {
		fmt.Fprintf(w, "kind %-11s %-5s subjects=%d red=%d amber=%d green=%d grey=%d findings=%d\n", k.Kind, k.Level, k.Subjects, k.Red, k.Amber, k.Green, k.Grey, k.Findings)
	}
	for _, p := range ev.Pillars {
		fmt.Fprintf(w, "pillar %-26s %-5s subjects=%d findings=%d waived=%d\n", p.Pillar, p.Level, p.Subjects, p.Findings, p.Waived)
	}
	for _, o := range ev.Owners {
		owner := o.Owner
		if owner == "" {
			owner = "(no owner)"
		}
		fmt.Fprintf(w, "owner %-20s %-5s subjects=%d findings=%d overdue=%d\n", owner, o.Level, o.Subjects, o.Findings, o.Overdue)
	}
	for _, r := range ev.Rules {
		if r.Fail+r.NoData+r.Waived == 0 && len(r.Unwritten) == 0 {
			continue
		}
		fmt.Fprintf(w, "rule %-30s %-9s pass=%d fail=%d nodata=%d na=%d waived=%d carried=%d %s\n", r.Rule, r.Phase, r.Pass, r.Fail, r.NoData, r.NA, r.Waived, r.Carried, strings.Join(r.Unwritten, ","))
	}
	for _, f := range ev.Findings {
		if f.State == judge.Draft || f.State == judge.Announced {
			fmt.Fprintf(w, "%-9s %-29s %s: %s\n", f.State, f.Rule, f.Subject, f.Detail)
		}
	}
	for _, wv := range ev.Waivers {
		if wv.Status == judge.WaiverRejected {
			fmt.Fprintf(w, "waiver ignored %s: %s\n", wv.Key(), wv.Problem)
		}
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
