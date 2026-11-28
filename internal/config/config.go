// Package config reads the board's one settings file. It says what the
// estate is called, how people sign in, which packs are on and with what
// parameters and scopes, the estate's positions (how levels roll up, how
// long a finding may stay open, who owns what, how long a waiver may run),
// which sources run in what order, and where findings go. Secrets never sit
// in it: ${NAME} is read from the environment. A source's own settings are
// its package's to read; this one only keeps them in order.
package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/orchard-suite/platformkit/v3/change"
	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/door"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/sources/orrery"
)

// Config is the whole file.
type Config struct {
	// Estate names what the board judges, such as acme.
	Estate string `yaml:"estate"`
	// PublicURL is where browsers reach the board.
	PublicURL string `yaml:"public_url"`
	// Refresh is the time between passes.
	Refresh Duration `yaml:"refresh"`
	Listen  struct {
		Session string `yaml:"session"`
		// Machine is the machine door: bearer tokens only, never identity
		// headers. It serves the probes, /healthz and /readyz, with no
		// token, and POST /v1/judge to the callers Machine names.
		Machine string `yaml:"machine"`
		// Health is the machine door's old name, from when it served the
		// probes alone; it sets the same address.
		Health string `yaml:"health"`
		// Profile is where Go's profiles of the process are served; nowhere
		// unless set.
		Profile string `yaml:"profile"`
		// Control is where trellis refresh asks the running board for a
		// pass. It must be a loopback address: whoever may exec into the
		// pod, or forward a port to it, may ask.
		Control string `yaml:"control"`
	} `yaml:"listen"`
	SignIn door.Config `yaml:"sign_in"`
	Packs  Packs       `yaml:"packs"`
	// Rollup is how the estate, kinds, pillars, owners, and packs take a
	// level from their subjects: worst, or share with thresholds.
	Rollup judge.Rollup `yaml:"rollup"`
	// Deadlines are how long an open finding of each severity may stay
	// open, such as critical: 7d.
	Deadlines map[estate.Severity]Duration `yaml:"deadlines"`
	// Owners say who answers for a subject: a CEL expression per kind,
	// and "*" for the rest, over the same variables a rule sees.
	Owners map[string]string `yaml:"owners"`
	// Waivers is what the estate allows of a waiver.
	Waivers estate.Policy `yaml:"waivers"`
	// Sources are read in order by the registry, each by its type.
	Sources []yaml.Node `yaml:"sources"`
	Sinks   struct {
		Orrery  *Orrery  `yaml:"orrery"`
		Webhook *Webhook `yaml:"webhook"`
	} `yaml:"sinks"`
	History *History `yaml:"history"`
	// Gate turns the judge on: the call on the machine door that routes
	// one change handed in.
	Gate *Gate `yaml:"gate"`
	// Machine names who may call the machine door, each with a bearer
	// token.
	Machine          Machine `yaml:"machine"`
	BrowserTelemetry struct {
		URL string `yaml:"url"`
		Key string `yaml:"key"`
	} `yaml:"browser_telemetry"`
	// Dir is the folder the file is in, for the relative paths in it.
	Dir string `yaml:"-"`
}

// Packs says which packs are on. How the estate tunes them, params and
// scopes, is the tuning file in Dir, so a pipeline's check reads the same.
type Packs struct {
	// Dir holds the estate's own packs and its tuning; a pack with a
	// built-in pack's id replaces it.
	Dir string `yaml:"dir"`
	// On names the packs judged, in order; every pack when empty.
	On []string `yaml:"on"`
	// Rollup sets a pack's own rollup, by pack.
	Rollup map[string]judge.Rollup `yaml:"rollup"`
}

// Orrery is a hub as a sink: it writes findings, verdicts, and packs into
// the blueprints Write names.
type Orrery struct {
	URL   string       `yaml:"url"`
	Token string       `yaml:"token"`
	Write orrery.Write `yaml:"write"`
	// Scorecards, when set, puts one hub scorecard per tiered pack.
	Scorecards *orrery.Scorecards `yaml:"scorecards"`
}

// Machine is who may call the machine door. A token is never the settings
// file's text: it is written as ${NAME}, filled from the environment, or
// read from token_file, a file the estate mounts.
type Machine struct {
	Callers []Caller `yaml:"callers"`
}

// Caller is one machine that may call the machine door: its name, which the
// logs say, and its token, as ${NAME} or in TokenFile.
type Caller struct {
	Name      string `yaml:"name"`
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
}

// MinToken is the shortest token a caller may have.
const MinToken = 32

var callerName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Tokens reads each caller's token: its ${NAME} value, or its file's first
// line, relative to dir. A token shorter than MinToken, or one two callers
// share, is refused.
func (m Machine) Tokens(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	seen := map[string]string{}
	for _, c := range m.Callers {
		tok := c.Token
		if c.TokenFile != "" {
			path := c.TokenFile
			if !filepath.IsAbs(path) {
				path = filepath.Join(dir, path)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("machine.callers %s: token_file: %w", c.Name, err)
			}
			tok, _, _ = strings.Cut(string(raw), "\n")
			tok = strings.TrimSpace(tok)
		}
		if len(tok) < MinToken {
			return nil, fmt.Errorf("machine.callers %s: its token is under %d characters", c.Name, MinToken)
		}
		if other, dup := seen[tok]; dup {
			return nil, fmt.Errorf("machine.callers %s and %s share a token; give each caller its own", other, c.Name)
		}
		seen[tok] = c.Name
		out[c.Name] = []byte(tok)
	}
	return out, nil
}

// Gate is the judge of changes handed in: which of the estate's gate packs
// in packs.dir count beside the built-in base pack, who decides when no
// rule names anyone, where the watch's proof is read, and which kind the
// catalog's safe paths are mapped as.
type Gate struct {
	// Packs names the estate's gate packs in packs.dir, in order. A draft
	// pack among them answers as a draft and never counts.
	Packs []string `yaml:"packs"`
	// DefaultApprovers decide when a fence sends a change to people and no
	// rule named anyone, in the runner's own terms for approvers: the
	// values of a field, on the entity or along a path from it.
	DefaultApprovers []change.Grant `yaml:"default_approvers"`
	// Annals is where proof is read, by key. Without it no disruption is
	// proven past claimed.
	Annals *Annals `yaml:"annals"`
	// SafePaths is the kind the catalog source maps the hub's safe paths
	// as, such as safe_path from the safe_paths include; none when empty.
	SafePaths string `yaml:"safe_paths"`
}

// Annals is the record the judge reads proof and replays from: its API
// port, and a token it issued, at the reader level.
type Annals struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// History is the PostgreSQL database trellis keeps its past in.
type History struct {
	URL string `yaml:"url"`
	// SignIn is password (the URL's) or azure (the pod's workload
	// identity's Entra token).
	SignIn        string `yaml:"sign_in"`
	RetentionDays int    `yaml:"retention_days"`
}

// Webhook posts each pass as JSON.
type Webhook struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// Duration reads Go duration text such as 15m, and days such as 7d.
type Duration time.Duration

// UnmarshalYAML reads "15m" or "7d".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// ParseDuration reads Go duration text, or a whole number of days.
func ParseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err == nil && n >= 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration such as 15m or 7d", s)
	}
	return v, nil
}

var envRef = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// Load reads the file at path, filling ${NAME} from the environment.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	c.Dir = filepath.Dir(path)
	return c, nil
}

// Parse reads the settings text. ${NAME} in a value is read from the
// environment; a comment is left as it is.
func Parse(raw []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := tokensFromEnv(&doc); err != nil {
		return nil, err
	}
	var missing []string
	var fill func(n *yaml.Node)
	fill = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			n.Value = envRef.ReplaceAllStringFunc(n.Value, func(m string) string {
				name := envRef.FindStringSubmatch(m)[1]
				v, ok := os.LookupEnv(name)
				if !ok {
					missing = append(missing, name)
				}
				return v
			})
		}
		for _, c := range n.Content {
			fill(c)
		}
	}
	fill(&doc)
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: the environment has no %v", missing)
	}
	var buf bytes.Buffer
	if err := yaml.NewEncoder(&buf).Encode(&doc); err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if c.Refresh == 0 {
		c.Refresh = Duration(15 * time.Minute)
	}
	if time.Duration(c.Refresh) < time.Minute {
		return nil, fmt.Errorf("config: refresh must be at least 1m")
	}
	if c.Listen.Session == "" {
		c.Listen.Session = ":8080"
	}
	switch {
	case c.Listen.Machine == "":
		c.Listen.Machine = c.Listen.Health
	case c.Listen.Health != "" && c.Listen.Health != c.Listen.Machine:
		return nil, fmt.Errorf("config: listen.health is the machine door's old name; set listen.machine alone")
	}
	if c.Listen.Machine == "" {
		c.Listen.Machine = ":8081"
	}
	c.Listen.Health = c.Listen.Machine
	if c.Listen.Control == "" {
		c.Listen.Control = DefaultControl
	}
	if err := loopback(c.Listen.Control); err != nil {
		return nil, fmt.Errorf("config: listen.control: %w", err)
	}
	if c.PublicURL == "" {
		c.PublicURL = "http://localhost:8080"
	}
	if len(c.Sources) == 0 {
		return nil, fmt.Errorf("config: no sources")
	}
	if err := c.Rollup.Check(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	for id, r := range c.Packs.Rollup {
		if err := r.Check(); err != nil {
			return nil, fmt.Errorf("config: packs.rollup.%s: %w", id, err)
		}
	}
	for sev := range c.Deadlines {
		if !sev.Valid() {
			return nil, fmt.Errorf("config: deadlines: %q is not critical, high, medium, or low", sev)
		}
	}
	for sev := range c.Waivers.MaxDays {
		if !sev.Valid() {
			return nil, fmt.Errorf("config: waivers.max_days: %q is not critical, high, medium, or low", sev)
		}
	}
	if c.Waivers.WarnDays == 0 {
		c.Waivers.WarnDays = 14
	}
	if o := c.Sinks.Orrery; o != nil {
		if err := o.Scorecards.Check(); err != nil {
			return nil, fmt.Errorf("config: sinks.orrery: %w", err)
		}
	}
	if h := c.History; h != nil {
		if h.URL == "" {
			return nil, fmt.Errorf("config: history needs url")
		}
		switch h.SignIn {
		case "":
			h.SignIn = "password"
		case "password", "azure":
		default:
			return nil, fmt.Errorf("config: history sign_in is password or azure, not %q", h.SignIn)
		}
		if h.RetentionDays == 0 {
			h.RetentionDays = 730
		}
		if h.RetentionDays < 30 {
			return nil, fmt.Errorf("config: history retention_days must be at least 30")
		}
	}
	if g := c.Gate; g != nil {
		if len(g.DefaultApprovers) == 0 {
			return nil, fmt.Errorf("config: gate.default_approvers: the judge will not start without them, since a fence sends a change to people and someone must be asked; name at least one grant, such as { field: team, values: [platform] }")
		}
		if err := change.ValidateRoute("gate.default_approvers", change.Route{To: change.RoutePeople, Grants: g.DefaultApprovers, Quorum: 1}, nil); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		if a := g.Annals; a != nil && (a.URL == "" || a.Token == "") {
			return nil, fmt.Errorf("config: gate.annals needs url and token")
		}
		if len(c.Machine.Callers) == 0 {
			return nil, fmt.Errorf("config: machine.callers: the judge is on the machine door, which admits only the callers named there with their tokens; name at least one")
		}
	}
	names := map[string]bool{}
	for _, cl := range c.Machine.Callers {
		switch {
		case !callerName.MatchString(cl.Name):
			return nil, fmt.Errorf("config: machine.callers: %q is not a lowercase name", cl.Name)
		case names[cl.Name]:
			return nil, fmt.Errorf("config: machine.callers names %s twice", cl.Name)
		case (cl.Token == "") == (cl.TokenFile == ""):
			return nil, fmt.Errorf("config: machine.callers %s: give token, as ${NAME}, or token_file, not both", cl.Name)
		}
		names[cl.Name] = true
	}
	return &c, nil
}

// tokensFromEnv refuses a caller's token written as text in the file: it is
// ${NAME}, filled from the environment, or a token_file.
func tokensFromEnv(doc *yaml.Node) error {
	root := doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	callers := field(field(root, "machine"), "callers")
	if callers == nil {
		return nil
	}
	for _, c := range callers.Content {
		if tok := field(c, "token"); tok != nil && !wholeRef.MatchString(tok.Value) {
			return fmt.Errorf("config: line %d: machine.callers token is written as ${NAME}, from the environment, or as a token_file; never as text in the settings", tok.Line)
		}
	}
	return nil
}

var wholeRef = regexp.MustCompile(`^\$\{[A-Z_][A-Z0-9_]*\}$`)

// field is a mapping node's value under key, or nil.
func field(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// Options are the estate's positions as the judge takes them, with set's
// parameters merged.
func (c *Config) Options(params judge.Params) (judge.Options, error) {
	owners, err := judge.NewOwners(c.Owners)
	if err != nil {
		return judge.Options{}, fmt.Errorf("config: owners: %w", err)
	}
	deadlines := map[estate.Severity]time.Duration{}
	for sev, d := range c.Deadlines {
		deadlines[sev] = time.Duration(d)
	}
	return judge.Options{Params: params, Rollup: c.Rollup, PackRollup: c.Packs.Rollup, Deadlines: deadlines, Owners: owners, Waivers: c.Waivers}, nil
}

// DefaultControl is where the control port listens unless the settings say.
const DefaultControl = "127.0.0.1:8082"

// loopback refuses an address any host but this one can reach.
func loopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%s is not a loopback address; use 127.0.0.1 or ::1", addr)
}
