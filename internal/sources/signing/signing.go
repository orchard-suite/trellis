// Package signing checks, for every image the estate runs, whether a
// trusted identity signed it with cosign and attested an SBOM for it.
// Keyless: the signature's certificate must come from the configured OIDC
// issuer and name an identity the configured pattern matches.
package signing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources"
)

// Signature is what verification found for an image, as the signing fact
// carries it.
type Signature struct {
	// InScope is false for an image nobody here is expected to sign.
	InScope bool `json:"in_scope"`
	Signed  bool `json:"signed"`
	// Identity is the certificate identity that signed it, when signed.
	Identity string `json:"identity"`
	SBOM     bool   `json:"sbom"`
	// Error is why verification could not run, if it could not; the source
	// records it as a miss, never as the fact.
	Error string `json:"-"`
}

// Verifier answers whether one image reference is signed and attested.
// The cosign-backed one is in cosign.go; tests use a fake.
type Verifier interface {
	Verify(ctx context.Context, reference string) Signature
}

// Provider is the signing source.
type Provider struct {
	verifier Verifier
	// scope matches the images a trusted identity is expected to sign.
	// The rest are recorded as out of scope and never fail a rule.
	scope       *regexp.Regexp
	concurrency int
	log         *slog.Logger
}

// New makes the provider. scope is a regular expression over image
// references; concurrency is how many images are verified at once.
func New(verifier Verifier, scope *regexp.Regexp, concurrency int, log *slog.Logger) *Provider {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Provider{verifier: verifier, scope: scope, concurrency: concurrency, log: log}
}

func init() {
	sources.Register("signing", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			Subjects    string `yaml:"subjects"`
			Identity    string `yaml:"identity"`
			Issuer      string `yaml:"issuer"`
			Concurrency int    `yaml:"concurrency"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		if c.Subjects == "" || c.Identity == "" || c.Issuer == "" {
			return nil, errors.New("the signing source needs subjects, identity, and issuer")
		}
		scope, err := regexp.Compile(c.Subjects)
		if err != nil {
			return nil, fmt.Errorf("subjects: %w", err)
		}
		if _, err := regexp.Compile(c.Identity); err != nil {
			return nil, fmt.Errorf("identity: %w", err)
		}
		v, err := NewCosign(Identity{Issuer: c.Issuer, SubjectRegExp: c.Identity})
		if err != nil {
			return nil, err
		}
		return New(v, scope, sources.OrDefault(c.Concurrency, 4), env.Log), nil
	})
}

// Facts are what the source writes.
func (p *Provider) Facts() []string { return []string{"signing"} }

// Collect verifies every image already in the estate. It adds none: the
// inventory provider says what runs, this one says whether it is signed.
func (p *Provider) Collect(ctx context.Context, e *estate.Writer) error {
	images := e.Of("image")
	refs := make([]string, 0, len(images))
	for _, img := range images {
		refs = append(refs, img.Key)
	}
	sort.Strings(refs)
	var inScope []string
	for _, ref := range refs {
		if p.scope.MatchString(ref) {
			inScope = append(inScope, ref)
		} else if err := e.Subject("image", ref, ref).Set("signing", Signature{InScope: false}); err != nil {
			return err
		}
	}
	results := make([]Signature, len(inScope))
	var wg sync.WaitGroup
	sem := make(chan struct{}, p.concurrency)
	for i, ref := range inScope {
		wg.Add(1)
		go func(i int, ref string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = p.verifier.Verify(ctx, ref)
		}(i, ref)
	}
	wg.Wait()
	var signed, attested, failed int
	for i, ref := range inScope {
		sig := results[i]
		sig.InScope = true
		h := e.Subject("image", ref, ref)
		if sig.Error != "" {
			failed++
			h.Miss("signing", "the signature could not be verified: "+sig.Error)
			p.log.WarnContext(ctx, "signature check failed", "image", ref, "error", sig.Error)
			continue
		}
		if err := h.Set("signing", sig); err != nil {
			return err
		}
		switch {
		case sig.Signed:
			signed++
			if sig.SBOM {
				attested++
			}
		}
	}
	p.log.InfoContext(ctx, "signing read", "images", len(refs), "in_scope", len(inScope), "signed", signed, "attested", attested, "failed", failed)
	return ctx.Err()
}
