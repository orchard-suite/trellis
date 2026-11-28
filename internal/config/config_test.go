package config

import (
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

const sample = `
# Secrets are ${NAME}, filled from the environment.
estate: acme
public_url: https://trellis.example.test
refresh: 10m
sign_in: { mode: headers, roles: [Viewer] }
rollup: { mode: share, green: 95, amber: 80 }
deadlines: { critical: 7d, high: 30d }
owners: { repository: 'runs.owners[0]', "*": '"platform"' }
waivers: { max_days: { critical: 30 }, warn_days: 7 }
packs:
  dir: /etc/trellis/packs
  on: [security, interfaces]
  rollup: { security: { mode: worst } }
sources:
  - type: orrery
    url: https://orrery.example.test
    token: ${TEST_ORRERY_TOKEN}
  - type: command
    name: scorecard
    run: [scorecard-facts, --all]
sinks:
  orrery: { url: https://orrery.example.test, token: "${TEST_ORRERY_TOKEN}" }
`

func TestParseReadsThePositionsAndFillsSecrets(t *testing.T) {
	t.Setenv("TEST_ORRERY_TOKEN", "tok")
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.Estate != "acme" || time.Duration(c.Refresh) != 10*time.Minute || c.Listen.Session != ":8080" || len(c.SignIn.Roles) != 1 {
		t.Errorf("config: %+v", c)
	}
	if len(c.Sources) != 2 || !strings.Contains(c.Sources[0].Content[5].Value, "tok") && c.Sinks.Orrery.Token != "tok" {
		t.Errorf("sources: %d, sink %+v", len(c.Sources), c.Sinks.Orrery)
	}
	if c.Rollup.Mode != "share" || time.Duration(c.Deadlines[estate.Critical]) != 7*24*time.Hour || c.Waivers.WarnDays != 7 {
		t.Errorf("positions: %+v %+v %+v", c.Rollup, c.Deadlines, c.Waivers)
	}
	o, err := c.Options(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.Deadlines[estate.High] != 30*24*time.Hour || o.Owners["*"] == nil || o.PackRollup["security"].Mode != "worst" {
		t.Errorf("options: %+v", o)
	}
}

func TestParseRefusesWhatItCannotRun(t *testing.T) {
	t.Setenv("TEST_ORRERY_TOKEN", "tok")
	cases := map[string]string{
		"unset secret":   strings.Replace(sample, "${TEST_ORRERY_TOKEN}", "${TEST_NOT_SET}", 1),
		"unknown field":  strings.Replace(sample, "refresh: 10m", "refresh: 10m\ncolour: blue", 1),
		"short refresh":  strings.Replace(sample, "refresh: 10m", "refresh: 5s", 1),
		"no sources":     "estate: acme\n",
		"bad rollup":     strings.Replace(sample, "green: 95, amber: 80", "green: 80, amber: 95", 1),
		"bad severity":   strings.Replace(sample, "critical: 7d", "urgent: 7d", 1),
		"bad deadline":   strings.Replace(sample, "critical: 7d", "critical: soon", 1),
		"bad owner expr": strings.Replace(sample, "runs.owners[0]", "runs.owners[", 1),
	}
	for name, doc := range cases {
		c, err := Parse([]byte(doc))
		if err == nil {
			_, err = c.Options(nil)
		}
		if err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestParseReadsTheScorecardsSetting(t *testing.T) {
	t.Setenv("TEST_ORRERY_TOKEN", "tok")
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.Sinks.Orrery.Scorecards != nil {
		t.Errorf("unset puts no scorecards: %+v", c.Sinks.Orrery.Scorecards)
	}
	on := strings.Replace(sample, `token: "${TEST_ORRERY_TOKEN}" }`, `token: "${TEST_ORRERY_TOKEN}", scorecards: { field: "trellis_{pack}_checks", id: "std_{pack}", packs: { recoverable: { id: backups, title: Backups } } } }`, 1)
	c, err = Parse([]byte(on))
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Sinks.Orrery.Scorecards; s == nil || s.Field != "trellis_{pack}_checks" || s.ID != "std_{pack}" || s.Packs["recoverable"].ID != "backups" || s.Packs["recoverable"].Title != "Backups" {
		t.Errorf("scorecards: %+v", s)
	}
	if _, err := Parse([]byte(strings.Replace(on, `field: "trellis_{pack}_checks", `, "", 1))); err == nil || !strings.Contains(err.Error(), "needs field") {
		t.Errorf("scorecards with no field: %v", err)
	}
}

func TestTheControlPortListensOnLoopbackOnly(t *testing.T) {
	t.Setenv("TEST_ORRERY_TOKEN", "tok")
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen.Control != DefaultControl {
		t.Errorf("control default: %q", c.Listen.Control)
	}
	for addr, ok := range map[string]bool{"127.0.0.1:9000": true, "[::1]:9000": true, "localhost:9000": true, ":9000": false, "0.0.0.0:9000": false, "10.0.0.4:9000": false} {
		_, err := Parse([]byte(sample + "listen: { control: \"" + addr + "\" }\n"))
		if (err == nil) != ok {
			t.Errorf("control %s: allowed %v, error %v", addr, ok, err)
		}
	}
}

// The judge will not start without default approvers: a fence sends a
// change to people, and someone must be asked.
func TestGateNeedsDefaultApprovers(t *testing.T) {
	t.Setenv("TEST_ORRERY_TOKEN", "tok")
	if _, err := Parse([]byte(sample + "gate: { packs: [acme-gate] }\n")); err == nil || !strings.Contains(err.Error(), "gate.default_approvers") {
		t.Fatalf("got %v", err)
	}
	if _, err := Parse([]byte(sample + "gate: { default_approvers: [{ field: team, values: [] }] }\n")); err == nil {
		t.Fatal("a grant with no values is refused")
	}
	if _, err := Parse([]byte(sample + "gate: { default_approvers: [{ field: team, values: [platform] }], annals: { url: https://annals.example.test } }\n")); err == nil {
		t.Fatal("annals needs a token")
	}
	c, err := Parse([]byte(sample + `gate:
  packs: [acme-gate]
  default_approvers: [{ field: team, values: [platform] }]
  annals: { url: https://annals.example.test, token: test-token }
  safe_paths: safe_path
`))
	if err != nil {
		t.Fatal(err)
	}
	if g := c.Gate; g.Packs[0] != "acme-gate" || g.DefaultApprovers[0].Values[0] != "platform" || g.Annals.Token != "test-token" || g.SafePaths != "safe_path" {
		t.Fatalf("gate %+v", g)
	}
}
