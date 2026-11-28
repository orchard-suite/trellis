package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/orchard-suite/platformkit/v3/annals"
	"github.com/orchard-suite/platformkit/v3/change"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/config"
	"github.com/orchard-suite/trellis/internal/gate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/obs"
)

// runJudge hands one statement to the running board's judge on its machine
// door, as a caller with its token, and prints the answer, so it is the
// very judgment the runner gets.
func runJudge(args []string) error {
	fs := flag.NewFlagSet("judge", flag.ContinueOnError)
	machine := fs.String("machine", "127.0.0.1:8081", "the running board's machine door (listen.machine)")
	tokenFile := fs.String("token-file", "", "a file holding a caller's token; $TRELLIS_TOKEN unless set")
	timeout := fs.Duration("timeout", time.Minute, "how long to wait for the answer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	token := os.Getenv("TRELLIS_TOKEN")
	if *tokenFile != "" {
		raw, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(raw))
	}
	if token == "" {
		return errors.New("judge: the machine door takes a caller's token: set TRELLIS_TOKEN or -token-file")
	}
	in := io.Reader(os.Stdin)
	if f := fs.Arg(0); f != "" && f != "-" {
		file, err := os.Open(f)
		if err != nil {
			return err
		}
		defer file.Close()
		in = file
	}
	raw, err := io.ReadAll(io.LimitReader(in, 8<<20))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	status, body, err := askJudge(ctx, http.DefaultClient, "http://"+*machine+"/v1/judge", token, raw)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if json.Indent(&out, body, "", "  ") != nil {
		out.Reset()
		out.Write(body)
	}
	fmt.Println(out.String())
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusBadRequest:
		return &failure{n: 1, floor: "statement"}
	}
	return fmt.Errorf("the judge answered %d", status)
}

// askJudge posts a statement, as it was written, to a judge, with a caller's
// token.
func askJudge(ctx context.Context, client *http.Client, url, token string, statement []byte) (int, []byte, error) {
	body, err := json.Marshal(struct {
		Statement json.RawMessage `json:"statement"`
	}{bytes.TrimSpace(statement)})
	if err != nil {
		return 0, nil, fmt.Errorf("the statement is not JSON: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("no board answered on its machine door: %w", err)
	}
	defer res.Body.Close()
	got, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	return res.StatusCode, got, err
}

// runReplay judges a gate pack over past changes read from Annals, joined
// to what each did, and says how many it would have asked of people and
// how many it would have passed that then did harm.
func runReplay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	path := configPath(fs)
	packID := fs.String("pack", "", "the gate pack to replay, by id: one of gate.packs, or of -packs-dir")
	drafts := fs.String("packs-dir", "", "a folder of gate packs to try, beside the configured ones")
	sinceText := fs.String("since", "30d", "the first change: RFC 3339, or a span back from now such as 30d")
	untilText := fs.String("until", "", "the end of the range, RFC 3339 or a span back; now when empty")
	catalog := fs.Bool("catalog", true, "run one pass of the sources first, so the pack's rules read the catalog as the judge does")
	asJSON := fs.Bool("json", false, "print the tally as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *packID == "" {
		return errors.New("replay: name the gate pack with -pack")
	}
	now := time.Now().UTC()
	since, err := when(*sinceText, now)
	if err != nil {
		return fmt.Errorf("replay -since: %w", err)
	}
	until := now
	if *untilText != "" {
		if until, err = when(*untilText, now); err != nil {
			return fmt.Errorf("replay -until: %w", err)
		}
	}
	if !since.Before(until) {
		return errors.New("replay: -since is before -until")
	}
	log := obs.NewLogger(os.Stderr, meter.Service, version, obs.ParseLevel(os.Getenv("LOG_LEVEL")))
	ctx := context.Background()
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if cfg.Gate == nil || cfg.Gate.Annals == nil {
		return errors.New("replay reads past changes from Annals: the settings need gate.annals")
	}
	j, err := newJudge(cfg, *drafts, log)
	if err != nil {
		return err
	}
	if *catalog {
		if err := readCatalog(ctx, cfg, j, log); err != nil {
			return err
		}
	}
	var records []annals.Event
	keep := func(ev annals.Event) error { records = append(records, ev); return nil }
	for _, q := range []annals.Query{
		{Verb: change.EventStated, Since: since, Until: until, Limit: 500},
		{Verb: change.EventEnded, Since: since, Limit: 500},
		{Verb: change.EventObserved, Since: since, Limit: 500},
	} {
		if err := j.annals.EachEvent(ctx, q, keep); err != nil {
			return fmt.Errorf("replay: read %s records: %w", q.Verb, err)
		}
	}
	t, err := j.Replay(ctx, gate.Join(records), *packID, j.annals, since, until)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(t); err != nil {
			return err
		}
	} else {
		printTally(os.Stdout, t)
	}
	if t.Misses > 0 {
		return &failure{n: t.Misses, floor: "miss"}
	}
	return nil
}

// readCatalog runs one pass of the settings' sources, as the board does,
// and gives the judge its catalog.
func readCatalog(ctx context.Context, cfg *config.Config, j *judgeOf, log *slog.Logger) error {
	set, err := loadPacks(cfg.Packs, "")
	if err != nil {
		return err
	}
	opts, err := cfg.Options(judge.Merge(set))
	if err != nil {
		return err
	}
	srcs, err := buildSources(cfg, log)
	if err != nil {
		return err
	}
	metrics, err := meter.New()
	if err != nil {
		return err
	}
	b := board.New(srcs, set, opts, metrics, log)
	b.Then(j.catalog.Update)
	snap := b.Refresh(ctx)
	log.Info("catalog read for the replay", "subjects", len(snap.Evaluation.Subjects), "took", snap.Took.Round(time.Millisecond).String())
	return nil
}

// when reads RFC 3339, or a span back from now such as 30d or 12h.
func when(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	d, err := config.ParseDuration(s)
	if err != nil {
		return time.Time{}, err
	}
	return now.Add(-d), nil
}

func printTally(w io.Writer, t gate.Tally) {
	fmt.Fprintf(w, "replay of %s over %s to %s: %d changes\n", t.Pack, t.Since.Format(time.RFC3339), t.Until.Format(time.RFC3339), t.Changes)
	fmt.Fprintf(w, "asked    %d of people\n", t.Asked)
	fmt.Fprintf(w, "passed   %d to no one\n", t.Passed)
	fmt.Fprintf(w, "misses   %d passed, and the watch then saw harm\n", t.Misses)
	fmt.Fprintf(w, "blind    %d passed, ran, and no watch could see what they did\n", t.Blind)
	fmt.Fprintf(w, "needless %d asked, ran, and the watch saw them clean\n", t.Needless)
	if t.Partial > 0 {
		fmt.Fprintf(w, "partial  %d stated before Annals kept unknown parts, so none of theirs was judged\n", t.Partial)
	}
}
