// Package keyvault reads the age of every secret in an Azure Key Vault, so
// a secret nobody has rotated shows on the board. It signs in as the
// namespace's workload identity; it never holds a client secret.
package keyvault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/azure"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/obs"
	"github.com/orchard-suite/trellis/internal/sources"
)

// TokenSource gives a Key Vault access token per run.
type TokenSource func(ctx context.Context) (string, error)

// Provider is the secrets provider for one vault.
type Provider struct {
	vaultURL string
	vault    string
	token    TokenSource
	http     *http.Client
	log      *slog.Logger
}

// New makes the provider for a vault URL such as https://example.vault.azure.net.
func New(vaultURL string, token TokenSource, log *slog.Logger) *Provider {
	vaultURL = strings.TrimRight(vaultURL, "/")
	name := strings.TrimPrefix(vaultURL, "https://")
	if dot := strings.Index(name, "."); dot > 0 {
		name = name[:dot]
	}
	return &Provider{
		vaultURL: vaultURL,
		vault:    name,
		token:    token,
		http:     obs.Client(60 * time.Second),
		log:      log,
	}
}

func init() {
	sources.Register("keyvault", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			URL string `yaml:"url"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		if c.URL == "" {
			return nil, errors.New("the keyvault source needs url")
		}
		id, err := azure.IdentityFromEnv()
		if err != nil {
			return nil, err
		}
		return New(c.URL, func(ctx context.Context) (string, error) { return azure.SignIn(ctx, id, azure.VaultScope) }, env.Log), nil
	})
}

// Facts are what the source writes.
func (p *Provider) Facts() []string { return []string{"secret"} }

// item is one secret as the vault lists it.
type item struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType"`
	Attributes  struct {
		Enabled bool  `json:"enabled"`
		Updated int64 `json:"updated"`
	} `json:"attributes"`
}

// Collect lists the vault's secrets with their last change.
func (p *Provider) Collect(ctx context.Context, e *estate.Writer) error {
	token, err := p.token(ctx)
	if err != nil {
		return err
	}
	next := p.vaultURL + "/secrets?api-version=7.4&maxresults=25"
	var items []item
	for next != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := p.http.Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
		res.Body.Close()
		if err != nil {
			return err
		}
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("key vault %s: %d %s", p.vault, res.StatusCode, strings.TrimSpace(string(body[:min(len(body), 200)])))
		}
		var page struct {
			Value    []item `json:"value"`
			NextLink string `json:"nextLink"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return err
		}
		items = append(items, page.Value...)
		next = page.NextLink
	}
	Shape(e, p.vault, items)
	p.log.InfoContext(ctx, "secrets read", "vault", p.vault, "secrets", len(items))
	return nil
}

// Shape adds one subject per secret. Its content type is the label as
// the vault holds it, and type is that label's kind: the words before a
// colon, lowercase, so "password: the NAS admin" is a password.
func Shape(e *estate.Writer, vault string, items []item) {
	for _, it := range items {
		name := it.ID[strings.LastIndex(it.ID, "/")+1:]
		if name == "" {
			continue
		}
		kind, _, _ := strings.Cut(it.ContentType, ":")
		_ = e.Subject("secret", vault+"/"+name, vault+"/"+name).Set("secret", map[string]any{
			"vault": vault, "name": name, "enabled": it.Attributes.Enabled,
			"updated":      time.Unix(it.Attributes.Updated, 0).UTC().Format(time.RFC3339),
			"content_type": it.ContentType,
			"type":         strings.ToLower(strings.TrimSpace(kind)),
		})
	}
}
