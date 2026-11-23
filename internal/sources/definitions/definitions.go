// Package definitions reads each API's definition from the link the catalog
// holds for it: a file on GitHub, read through the API with the forge's
// token so a private repository works, or any other URL, read as it is.
// It adds to the APIs the inventory provider found, so it runs after it.
package definitions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/obs"
	"github.com/orchard-suite/trellis/internal/openapi"
	"github.com/orchard-suite/trellis/internal/sources"
)

// MaxSize is the largest definition read.
const MaxSize = 16 << 20

// Provider is the definitions source.
type Provider struct {
	githubAPI   string
	token       string
	concurrency int
	http        *http.Client
	log         *slog.Logger
}

// Config is the provider's settings.
type Config struct {
	// GitHubAPI is the REST base, https://api.github.com unless set.
	GitHubAPI string
	// GitHubToken reads definitions in private repositories.
	GitHubToken string
	Concurrency int
}

// New makes the provider.
func New(cfg Config, log *slog.Logger) *Provider {
	if cfg.GitHubAPI == "" {
		cfg.GitHubAPI = "https://api.github.com"
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	return &Provider{githubAPI: strings.TrimRight(cfg.GitHubAPI, "/"), token: cfg.GitHubToken, concurrency: cfg.Concurrency, http: obs.Client(30 * time.Second), log: log}
}

func init() {
	sources.Register("definitions", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			GitHubAPI   string `yaml:"github_api"`
			GitHubToken string `yaml:"github_token"`
			Concurrency int    `yaml:"concurrency"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		return New(Config{GitHubAPI: c.GitHubAPI, GitHubToken: c.GitHubToken, Concurrency: c.Concurrency}, env.Log), nil
	})
}

// Facts are what the source writes.
func (p *Provider) Facts() []string { return []string{"definition"} }

// Collect reads every linked definition once, however many APIs share it,
// into each API's definition fact. A definition that cannot be read is
// recorded on its APIs; the run fails only when none could be.
func (p *Provider) Collect(ctx context.Context, e *estate.Writer) error {
	byLink := map[string][]*estate.Handle{}
	for _, s := range e.Of("api") {
		v, _ := s.Value("api")
		api, _ := v.(map[string]any)
		if link, _ := api["spec"].(string); link != "" {
			byLink[link] = append(byLink[link], e.Subject(s.Kind, s.Key, s.Title))
		}
	}
	var (
		mu     sync.Mutex
		failed []string
		wg     sync.WaitGroup
		slots  = make(chan struct{}, p.concurrency)
	)
	for link, apis := range byLink {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			def := p.read(ctx, link)
			mu.Lock()
			defer mu.Unlock()
			if def.Error != "" {
				failed = append(failed, link)
				p.log.WarnContext(ctx, "definition unread", "link", link, "error", def.Error)
			}
			for _, a := range apis {
				_ = a.Set("definition", def)
			}
		}()
	}
	wg.Wait()
	p.log.InfoContext(ctx, "definitions read", "definitions", len(byLink), "failed", len(failed))
	if len(byLink) > 0 && len(failed) == len(byLink) {
		return fmt.Errorf("no definition could be read; first: %s", failed[0])
	}
	return nil
}

// Definition is what an API's definition fact carries: its language, why it
// could not be read, and, for OpenAPI, what the shape rules judge.
type Definition struct {
	Format  string         `json:"format"`
	Error   string         `json:"error"`
	OpenAPI *openapi.Facts `json:"openapi,omitempty"`
}

// read fetches and parses one definition.
func (p *Provider) read(ctx context.Context, link string) Definition {
	data, err := p.fetch(ctx, link)
	if err != nil {
		return Definition{Error: err.Error()}
	}
	return Parse(link, data)
}

// Parse reads a definition's bytes, naming its language when it is not
// OpenAPI. The link's extension tells protobuf and GraphQL apart.
func Parse(link string, data []byte) Definition {
	switch strings.ToLower(path.Ext(strings.SplitN(link, "?", 2)[0])) {
	case ".proto":
		return Definition{Format: "protobuf"}
	case ".graphql", ".graphqls", ".gql":
		return Definition{Format: "GraphQL"}
	}
	doc, err := openapi.Parse(data)
	if errors.Is(err, openapi.ErrNotOpenAPI) {
		return Definition{Format: language(data)}
	}
	if err != nil {
		return Definition{Error: err.Error()}
	}
	f := doc.Facts()
	return Definition{Format: "openapi", OpenAPI: &f}
}

var asyncAPI = regexp.MustCompile(`(?m)^\s*"?asyncapi"?\s*:`)

func language(data []byte) string {
	if asyncAPI.Match(data) {
		return "AsyncAPI"
	}
	return "an unknown language"
}

// blob is a file's link on GitHub: owner, repository, ref, path.
var blob = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/blob/([^/]+)/(.+)$`)

func (p *Provider) fetch(ctx context.Context, link string) ([]byte, error) {
	target, accept, auth := link, "application/json, application/yaml;q=0.9, */*;q=0.5", false
	if m := blob.FindStringSubmatch(link); m != nil {
		segs := strings.Split(m[4], "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		target = fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", p.githubAPI, url.PathEscape(m[1]), url.PathEscape(m[2]), strings.Join(segs, "/"), url.QueryEscape(m[3]))
		accept, auth = "application/vnd.github.raw", true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if auth {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if p.token != "" {
			req.Header.Set("Authorization", "Bearer "+p.token)
		}
	}
	res, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", host(target), res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, fmt.Errorf("it is over %d MiB", MaxSize>>20)
	}
	return data, nil
}

func host(u string) string {
	if parsed, err := url.Parse(u); err == nil {
		return parsed.Host
	}
	return u
}
