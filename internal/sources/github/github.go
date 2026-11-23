// Package github reads each repository from GitHub: whether it is
// private, every path on its default branch, and what its build files say
// (the build fact, shaped by buildfiles). A build file is fetched by its
// blob's hash and kept, so a pass fetches only files that changed. It adds
// facts to the repositories the inventory provider found, so it runs after
// it.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/orchard-suite/trellis/internal/buildfiles"
	"github.com/orchard-suite/trellis/internal/obs"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources"
)

// Provider is the github source.
type Provider struct {
	api         string
	token       string
	owners      map[string]bool
	concurrency int
	http        *http.Client
	log         *slog.Logger

	mu    sync.Mutex
	blobs map[string][]byte
}

// Config is the provider's settings.
type Config struct {
	// API is the REST base, https://api.github.com unless set.
	API   string
	Token string
	// Owners are the accounts whose repositories are read. The rest are
	// left unread, so the build rules say nothing of them.
	Owners      []string
	Concurrency int
}

// New makes the provider.
func New(cfg Config, log *slog.Logger) *Provider {
	if cfg.API == "" {
		cfg.API = "https://api.github.com"
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	owners := map[string]bool{}
	for _, o := range cfg.Owners {
		owners[strings.ToLower(o)] = true
	}
	return &Provider{api: strings.TrimRight(cfg.API, "/"), token: cfg.Token, owners: owners, concurrency: cfg.Concurrency, http: obs.Client(30 * time.Second), log: log, blobs: map[string][]byte{}}
}

func init() {
	sources.Register("github", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			API         string   `yaml:"api"`
			Token       string   `yaml:"token"`
			Owners      []string `yaml:"owners"`
			Concurrency int      `yaml:"concurrency"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		if len(c.Owners) == 0 {
			return nil, errors.New("the github source needs owners, the accounts whose repositories it reads")
		}
		return New(Config{API: c.API, Token: c.Token, Owners: c.Owners, Concurrency: c.Concurrency}, env.Log), nil
	})
}

// Facts are what the source writes.
func (p *Provider) Facts() []string { return []string{"forge", "build"} }

// repo is where a repository subject sits on the forge, from its repo
// fact.
type repo struct {
	subject             *estate.Handle
	owner, name, branch string
}

func repoOf(w *estate.Writer, s *estate.Subject) (repo, bool) {
	v, _ := s.Value("repo")
	f, _ := v.(map[string]any)
	str := func(k string) string { v, _ := f[k].(string); return v }
	archived, _ := f["archived"].(bool)
	fork, _ := f["fork"].(bool)
	r := repo{subject: w.Subject(s.Kind, s.Key, s.Title), owner: str("owner"), name: str("name"), branch: str("branch")}
	return r, f != nil && !archived && !fork && r.owner != "" && r.name != ""
}

// Collect reads the tree of every live repository of the owners into its
// forge fact. A repository that cannot be read keeps its tree unknown; the
// run fails only when none could be.
func (p *Provider) Collect(ctx context.Context, e *estate.Writer) error {
	var todo []repo
	for _, s := range e.Of("repository") {
		if r, ok := repoOf(e, s); ok && p.owners[strings.ToLower(r.owner)] {
			todo = append(todo, r)
		}
	}
	var (
		mu     sync.Mutex
		failed []string
		wg     sync.WaitGroup
		slots  = make(chan struct{}, p.concurrency)
	)
	for _, r := range todo {
		wg.Add(1)
		slots <- struct{}{}
		go func(r repo) {
			defer wg.Done()
			defer func() { <-slots }()
			private, perr := p.private(ctx, r)
			blobs, err := p.tree(ctx, r)
			var build buildfiles.Build
			var berr error
			if err == nil {
				build, berr = buildfiles.Read(paths(blobs), func(path string) ([]byte, error) { return p.blob(ctx, r, blobs[path]) })
			}
			mu.Lock()
			defer mu.Unlock()
			forge := map[string]any{}
			if perr == nil {
				forge["private"] = private
			} else {
				p.log.WarnContext(ctx, "github visibility unread", "repository", r.owner+"/"+r.name, "error", perr)
			}
			if errors.Is(err, errEmpty) {
				forge["empty"], forge["files"] = true, []string{}
				_ = r.subject.Set("forge", forge)
				return
			}
			if err != nil {
				failed = append(failed, r.owner+"/"+r.name)
				r.subject.Miss("forge", "the tree could not be read: "+err.Error())
				r.subject.Miss("build", "the tree could not be read")
				p.log.WarnContext(ctx, "github tree unread", "repository", r.owner+"/"+r.name, "error", err)
				return
			}
			forge["empty"], forge["files"] = false, paths(blobs)
			_ = r.subject.Set("forge", forge)
			if berr != nil {
				r.subject.Miss("build", "a build file could not be read: "+berr.Error())
				p.log.WarnContext(ctx, "github build files unread", "repository", r.owner+"/"+r.name, "error", berr)
				return
			}
			_ = r.subject.Set("build", build)
		}(r)
	}
	wg.Wait()
	p.log.InfoContext(ctx, "github read", "repositories", len(todo), "failed", len(failed))
	if len(todo) > 0 && len(failed) == len(todo) {
		return fmt.Errorf("no repository tree could be read; first: %s", failed[0])
	}
	return nil
}

// errEmpty is GitHub's answer for a repository with no commits: 409.
var errEmpty = errors.New("the repository is empty")

type treeResponse struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"tree"`
	Truncated bool `json:"truncated"`
}

// paths is the tree's files, in the forge's order.
func paths(blobs map[string]string) []string {
	out := make([]string, 0, len(blobs))
	for p := range blobs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (p *Provider) get(ctx context.Context, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	return p.http.Do(req)
}

// private reads whether the repository is private.
func (p *Provider) private(ctx context.Context, r repo) (bool, error) {
	res, err := p.get(ctx, fmt.Sprintf("%s/repos/%s/%s", p.api, url.PathEscape(r.owner), url.PathEscape(r.name)), "application/vnd.github+json")
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("github answered %s", res.Status)
	}
	var body struct {
		Private *bool `json:"private"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return false, err
	}
	if body.Private == nil {
		return false, errors.New("the answer has no private field")
	}
	return *body.Private, nil
}

// blob reads one file by its hash, once: a hash names its content, so a
// kept copy never goes stale.
func (p *Provider) blob(ctx context.Context, r repo, sha string) ([]byte, error) {
	p.mu.Lock()
	b, ok := p.blobs[sha]
	p.mu.Unlock()
	if ok {
		return b, nil
	}
	res, err := p.get(ctx, fmt.Sprintf("%s/repos/%s/%s/git/blobs/%s", p.api, url.PathEscape(r.owner), url.PathEscape(r.name), url.PathEscape(sha)), "application/vnd.github.raw+json")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github answered %s", res.Status)
	}
	b, err = io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.blobs[sha] = b
	p.mu.Unlock()
	return b, nil
}

// tree lists every file on the default branch, by path, with its blob's
// hash.
func (p *Provider) tree(ctx context.Context, r repo) (map[string]string, error) {
	ref := r.branch
	if ref == "" {
		ref = "HEAD"
	}
	u := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", p.api, url.PathEscape(r.owner), url.PathEscape(r.name), url.PathEscape(ref))
	res, err := p.get(ctx, u, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		return nil, errEmpty
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github answered %s", res.Status)
	}
	var body treeResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Truncated {
		return nil, errors.New("the tree is too large to list in one request")
	}
	files := make(map[string]string, len(body.Tree))
	for _, t := range body.Tree {
		if t.Type == "blob" {
			files[t.Path] = t.SHA
		}
	}
	return files, nil
}
