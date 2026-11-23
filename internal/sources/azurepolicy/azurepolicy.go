// Package azurepolicy reads Azure Policy as a source that brings its own
// rules. Azure judges its resources; trellis does not judge them again. It
// reads Azure's policy definitions as rules, one per definition with each
// initiative flattened into its members, Azure's compliance states as each
// resource's verdicts, and Azure's exemptions as waivers, and hands the
// board a pack of those rules each pass. Each rule's check reads Azure's
// verdict and nothing else. The waivers are weighed by the estate's policy
// like any other, so an exemption with no end or no owner does not count.
//
// States, assignments, and exemptions come from Resource Graph, scoped to
// management groups so it sees what is assigned and exempted at a group;
// definitions and initiatives come from the management API by id, kept a
// day. The pod's workload identity needs Reader at each management group.
package azurepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/azure"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/obs"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/sources"
)

// The queries a pass runs. Each projects only what Shape reads: a mid-size
// estate holds tens of thousands of states, and Resource Graph throttles.
const (
	statesQuery = `policyresources
| where type =~ 'microsoft.policyinsights/policystates'
| where tobool(properties.isDeleted) != true
| project id, resource = tolower(tostring(properties.resourceId)),
  state = tostring(properties.complianceState),
  under = tostring(properties.stateDetails.complianceSubState),
  assignment = tolower(tostring(properties.policyAssignmentId)),
  definition = tolower(tostring(properties.policyDefinitionId)),
  name = tostring(properties.policyDefinitionName),
  reference = tolower(tostring(properties.policyDefinitionReferenceId)),
  set = tolower(tostring(properties.policySetDefinitionId)),
  setName = tostring(properties.policySetDefinitionName),
  groups = properties.policyDefinitionGroupNames,
  managementGroups = tolower(tostring(properties.managementGroupIds)),
  location = tolower(tostring(properties.resourceLocation)),
  resourceType = tolower(tostring(properties.resourceType))`
	assignmentsQuery = `policyresources
| where type =~ 'microsoft.authorization/policyassignments'
| project id = tolower(id), name = tostring(properties.displayName),
  definition = tolower(tostring(properties.policyDefinitionId))`
	exemptionsQuery = `policyresources
| where type =~ 'microsoft.authorization/policyexemptions'
| project id, name = tostring(properties.displayName),
  description = tostring(properties.description),
  assignment = tolower(tostring(properties.policyAssignmentId)),
  references = properties.policyDefinitionReferenceIds,
  category = tostring(properties.exemptionCategory),
  expiresOn = tostring(properties.expiresOn),
  metadata = properties.metadata,
  selectors = properties.resourceSelectors`
)

// definitionsAPI is the management API version definitions and
// initiatives are read at.
const definitionsAPI = "2023-04-01"

// keep is how long a definition read by id is reused before it is read
// again. Definitions change seldom, and a pass reads every one it names.
const keep = 24 * time.Hour

// readers is how many definitions are read at once.
const readers = 4

// Config is the source's settings.
type Config struct {
	Endpoint    string `yaml:"endpoint"`
	azure.Scope `yaml:",inline"`
	// Kind is the kind of subject an Azure resource is, the one the
	// catalog's Azure resources are mapped as, so a finding links to its
	// entity; azure_resource unless set.
	Kind string `yaml:"kind"`
	// Fact is the fact each resource's verdicts are written as;
	// azure_policy unless set.
	Fact string `yaml:"fact"`
	// Pack, Title, and Family are the pack's: azure-policy, Azure Policy,
	// and security unless set.
	Pack   string `yaml:"pack"`
	Title  string `yaml:"title"`
	Family string `yaml:"family"`
	// Standard and Pillar are what every rule enforces and serves.
	Standard string   `yaml:"standard"`
	Pillar   string   `yaml:"pillar"`
	Tiers    []string `yaml:"tiers"`
	Tuning   Tuning   `yaml:"tuning"`
	// Exemptions name the metadata keys of an exemption that hold its owner
	// and, for a mitigated one, its control. Azure has no fields for them.
	Exemptions struct {
		Owner   string `yaml:"owner"`
		Control string `yaml:"control"`
	} `yaml:"exemptions"`
	Frameworks map[string]string `yaml:"frameworks"`
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Settings checks the config and fills its defaults.
func (c Config) Settings(source string) (Settings, error) {
	or := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	s := Settings{
		Source: source, Pack: or(c.Pack, "azure-policy"), Title: or(c.Title, "Azure Policy"), Family: or(c.Family, "security"),
		Kind: or(c.Kind, "azure_resource"), Fact: or(c.Fact, "azure_policy"),
		Standard: or(c.Standard, "baseline-policies-bind-by-default-a-deviation-needs-a-writte"), Pillar: or(c.Pillar, "platform/security"),
		Tiers: c.Tiers, Tuning: c.Tuning, Owner: c.Exemptions.Owner, Control: c.Exemptions.Control, Frameworks: c.Frameworks,
	}
	if s.Tuning.Default.Severity == "" {
		s.Tuning.Default.Severity = estate.Medium
	}
	if err := c.Scope.Check(); err != nil {
		return s, err
	}
	if !nameRE.MatchString(s.Kind) || !nameRE.MatchString(s.Fact) {
		return s, fmt.Errorf("kind %q and fact %q must be lowercase names", s.Kind, s.Fact)
	}
	if len(s.Tiers) > 0 && s.Tuning.Default.Tier == "" {
		return s, fmt.Errorf("the pack has tiers %v, so tuning.default names the tier every rule takes unless another says", s.Tiers)
	}
	levels := map[string]Level{"default": s.Tuning.Default}
	for k, l := range s.Tuning.Categories {
		levels["category "+k] = l
	}
	for k, l := range s.Tuning.Definitions {
		levels["definition "+k] = l
	}
	for at, l := range levels {
		if l.Severity != "" && !l.Severity.Valid() {
			return s, fmt.Errorf("tuning %s: severity %q is not critical, high, medium, or low", at, l.Severity)
		}
		if l.Tier != "" && !contains(s.Tiers, l.Tier) {
			return s, fmt.Errorf("tuning %s: tier %q is not one of the tiers %v", at, l.Tier, s.Tiers)
		}
	}
	return s, nil
}

// Source reads Azure Policy.
type Source struct {
	api      *azure.Management
	scope    azure.Scope
	settings Settings
	log      *slog.Logger

	mu    sync.Mutex
	packs []*pack.Pack
	defs  map[string]cached[Definition]
	sets  map[string]cached[Set]
}

// cached is one read by id and when it was made; found is false for an id
// Azure does not have.
type cached[T any] struct {
	value T
	found bool
	at    time.Time
}

// New makes the source.
func New(api *azure.Management, scope azure.Scope, settings Settings, log *slog.Logger) *Source {
	return &Source{api: api, scope: scope, settings: settings, log: log, defs: map[string]cached[Definition]{}, sets: map[string]cached[Set]{}}
}

func init() {
	sources.Register("azure_policy", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c Config
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		s, err := c.Settings(env.Name)
		if err != nil {
			return nil, fmt.Errorf("azure_policy: %w", err)
		}
		id, err := azure.IdentityFromEnv()
		if err != nil {
			return nil, err
		}
		api := azure.NewManagement(c.Endpoint, func(ctx context.Context) (string, error) { return azure.SignIn(ctx, id, azure.ManagementScope) }, obs.Client(60*time.Second))
		return New(api, c.Scope, s, env.Log), nil
	})
}

// Facts are the facts it writes.
func (s *Source) Facts() []string { return []string{s.settings.Fact} }

// Packs are the pack its last full read made.
func (s *Source) Packs() []*pack.Pack {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.packs
}

// Collect reads Azure Policy, writes each resource's verdicts and the
// waivers Azure's exemptions make, and keeps the pack for the board. A read
// that fails fails the pass, and the board carries what the last one read.
func (s *Source) Collect(ctx context.Context, w *estate.Writer) error {
	read, err := s.read(ctx)
	if err != nil {
		return err
	}
	shaped, err := Shape(read, s.settings)
	if err != nil {
		return err
	}
	for _, why := range shaped.Skipped {
		s.log.WarnContext(ctx, "azure policy left out", "why", why)
	}
	n := Write(w, s.settings, shaped)
	s.mu.Lock()
	s.packs = []*pack.Pack{shaped.Pack}
	s.mu.Unlock()
	s.log.InfoContext(ctx, "azure policy read", "states", len(read.States), "assignments", len(read.Assignments), "exemptions", len(read.Exemptions), "rules", len(shaped.Pack.Rules), "resources", n, "waivers", len(shaped.Waivers))
	return nil
}

// Write puts the shaped reading on the estate: each resource's verdicts as
// one fact on the subject of the settings' kind, and an empty one on every
// other subject of that kind a source before it brought, which no policy
// judges, so the rules do not apply to it rather than lacking data. It
// says how many resources Azure judged.
func Write(w *estate.Writer, s Settings, shaped Shaped) int {
	keys := make([]string, 0, len(shaped.Verdicts))
	for k := range shaped.Verdicts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_ = w.Subject(s.Kind, key, key[strings.LastIndex(key, "/")+1:]).Set(s.Fact, shaped.Verdicts[key])
	}
	for _, sub := range w.Of(s.Kind) {
		if _, judged := shaped.Verdicts[sub.Key]; !judged {
			_ = w.Subject(s.Kind, sub.Key, sub.Title).Set(s.Fact, map[string]Verdict{})
		}
	}
	w.AddWaivers(shaped.Waivers)
	return len(keys)
}

func (s *Source) read(ctx context.Context) (Read, error) {
	var r Read
	if err := s.query(ctx, statesQuery, &r.States); err != nil {
		return r, fmt.Errorf("policy states: %w", err)
	}
	if err := s.query(ctx, assignmentsQuery, &r.Assignments); err != nil {
		return r, fmt.Errorf("policy assignments: %w", err)
	}
	if err := s.query(ctx, exemptionsQuery, &r.Exemptions); err != nil {
		return r, fmt.Errorf("policy exemptions: %w", err)
	}
	var defs, sets []string
	for _, a := range r.Assignments {
		if strings.Contains(a.Definition, "/policysetdefinitions/") {
			sets = append(sets, a.Definition)
		} else {
			defs = append(defs, a.Definition)
		}
	}
	var err error
	if r.Sets, err = readAll(ctx, s, s.sets, sets); err != nil {
		return r, fmt.Errorf("initiatives: %w", err)
	}
	for _, set := range r.Sets {
		for _, m := range set.Properties.PolicyDefinitions {
			defs = append(defs, strings.ToLower(m.PolicyDefinitionID))
		}
	}
	for _, st := range r.States {
		defs = append(defs, st.Definition)
	}
	if r.Definitions, err = readAll(ctx, s, s.defs, defs); err != nil {
		return r, fmt.Errorf("definitions: %w", err)
	}
	return r, nil
}

// query runs one Resource Graph query into out. Each of the three projects
// id, which keys its rows.
func (s *Source) query(ctx context.Context, q string, out any) error {
	rows, err := s.api.Query(ctx, s.scope, q, "id")
	if err != nil {
		return err
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// readAll reads each id once, a few at a time, reusing what was read
// within a day. An id Azure does not have is left out: a rule still comes
// from its states, by the definition's name. Any other failure fails the
// read.
func readAll[T any](ctx context.Context, s *Source, cache map[string]cached[T], ids []string) (map[string]T, error) {
	now := time.Now()
	var want []string
	seen := map[string]bool{}
	s.mu.Lock()
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if c, ok := cache[id]; !ok || now.Sub(c.at) > keep {
			want = append(want, id)
		}
	}
	s.mu.Unlock()

	jobs := make(chan string)
	errs := make(chan error, len(want))
	var wg sync.WaitGroup
	for range min(readers, len(want)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				var v T
				err := s.api.Get(ctx, id, definitionsAPI, &v)
				if err != nil && !errors.Is(err, azure.ErrNotFound) {
					errs <- err
					continue
				}
				s.mu.Lock()
				cache[id] = cached[T]{value: v, found: err == nil, at: now}
				s.mu.Unlock()
			}
		}()
	}
	for _, id := range want {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return nil, err
	}
	out := map[string]T{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range seen {
		if c := cache[id]; c.found {
			out[id] = c.value
		}
	}
	return out, nil
}
