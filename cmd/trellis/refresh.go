package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/orchard-suite/trellis/internal/config"
	"github.com/orchard-suite/trellis/internal/server"
	"github.com/orchard-suite/trellis/internal/sources"
)

// runRefresh asks the running board for a pass on its control port, waits
// for the pass and its sends, and says how each went.
func runRefresh(args []string) error {
	fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
	control := fs.String("control", config.DefaultControl, "the running board's control address (listen.control)")
	timeout := fs.Duration("timeout", 20*time.Minute, "how long to wait for the pass")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	pass, err := askPass(ctx, http.DefaultClient, "http://"+*control+"/v1/refresh")
	if err != nil {
		return err
	}
	return reportPass(os.Stdout, pass)
}

func askPass(ctx context.Context, client *http.Client, url string) (*server.Pass, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("no board answered on its control port: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("the board answered %s: %s", res.Status, body)
	}
	var pass server.Pass
	if err := json.NewDecoder(res.Body).Decode(&pass); err != nil {
		return nil, fmt.Errorf("the board's answer: %w", err)
	}
	return &pass, nil
}

// reportPass prints the pass and fails when a source or a sink did.
func reportPass(w io.Writer, p *server.Pass) error {
	fmt.Fprintf(w, "pass at %s took %s: estate %s, %d open\n", p.RefreshedAt.Format(time.RFC3339), (time.Duration(p.TookMS) * time.Millisecond).Round(time.Second), p.Level, p.Open)
	bad := 0
	for _, r := range p.Sources {
		fmt.Fprintf(w, "source %-12s %-6s %s\n", r.Source, r.Outcome, r.Error)
		if r.Outcome == sources.Failed {
			bad++
		}
	}
	if len(p.Sent) == 0 {
		fmt.Fprintln(w, "no sinks: nothing was sent")
	}
	for _, s := range p.Sent {
		outcome := "sent"
		if !s.OK {
			outcome = "failed"
			bad++
		}
		fmt.Fprintf(w, "sink   %-12s %-6s %s\n", s.Sink, outcome, s.Error)
	}
	if bad > 0 {
		return &failure{n: bad, floor: "pass"}
	}
	return nil
}
