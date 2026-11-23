// Package relay passes the board page's browser telemetry on. The page
// posts Grafana Faro batches to its own server (POST /api/telemetry), never
// to the collector, so the collector's key never leaves the server and no
// batch can claim another app or another person.
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/orchard-suite/trellis/internal/door"
	"github.com/orchard-suite/trellis/internal/obs"
)

// MaxBatchBytes caps a batch, per the telemetry standard.
const MaxBatchBytes = 256 << 10

// Config names the app and the collector. Without URL or Key every batch
// is accepted and dropped: telemetry never fails the page.
type Config struct {
	// Service is the app's service name; batches are labelled
	// `<service>-web` and counted in `<service>.browser.batches`.
	Service string
	Version string
	URL     string
	Key     string
}

// Relay is the page's telemetry endpoint.
type Relay struct {
	cfg     Config
	http    *http.Client
	log     *slog.Logger
	batches metric.Int64Counter
}

// NewRelay registers the batch counter on the global meter provider, so
// call it after telemetry.Setup.
func NewRelay(cfg Config, log *slog.Logger) (*Relay, error) {
	batches, err := otel.Meter(cfg.Service).Int64Counter(cfg.Service+".browser.batches",
		metric.WithDescription("Browser telemetry batches, by outcome: relayed, dropped (no key), failed, or refused (too large or not JSON)."))
	if err != nil {
		return nil, err
	}
	return &Relay{cfg: cfg, http: obs.Client(5 * time.Second), log: log, batches: batches}, nil
}

// ServeHTTP takes one batch, makes it the server's (this app's web client,
// and the person a door gate admitted or nobody), and relays it with the
// key. It answers 202 whatever the collector does, 413 for a batch over
// the cap, and 400 for one that is not a JSON object.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, MaxBatchBytes))
	if err != nil {
		r.count(ctx, "refused")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	batch, ok := Own(body, r.cfg.Service+"-web", r.cfg.Version, door.From(ctx).Email)
	if !ok {
		r.count(ctx, "refused")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	if r.cfg.URL == "" || r.cfg.Key == "" {
		r.count(ctx, "dropped")
		return
	}
	if err := r.send(ctx, batch); err != nil {
		r.count(ctx, "failed")
		r.log.WarnContext(ctx, "browser telemetry relay failed", "error", err)
		return
	}
	r.count(ctx, "relayed")
}

func (r *Relay) count(ctx context.Context, outcome string) {
	r.batches.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// Own parses a batch and makes it the server's: meta.app names app and
// version whatever the browser said, and meta.user is the signed-in email,
// or is removed when there is none. It returns false for anything that is
// not a JSON object.
func Own(body []byte, app, version, email string) ([]byte, bool) {
	var batch map[string]any
	if json.Unmarshal(body, &batch) != nil || batch == nil {
		return nil, false
	}
	meta, _ := batch["meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	appMeta, _ := meta["app"].(map[string]any)
	if appMeta == nil {
		appMeta = map[string]any{}
	}
	appMeta["name"] = app
	appMeta["version"] = version
	meta["app"] = appMeta
	if email != "" {
		meta["user"] = map[string]any{"email": email}
	} else {
		delete(meta, "user")
	}
	batch["meta"] = meta
	out, err := json.Marshal(batch)
	return out, err == nil
}

type statusError int

func (e statusError) Error() string { return "collector answered " + http.StatusText(int(e)) }

func (r *Relay) send(ctx context.Context, batch []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.URL, bytes.NewReader(batch))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", r.cfg.Key)
	res, err := r.http.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return statusError(res.StatusCode)
	}
	return nil
}
