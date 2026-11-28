# trellis

One judge over an estate's standards: packs of rules kept as data, judged
over subjects that sources read from what the estate already knows, with
one verdict per subject, pack, kind, pillar, and the estate. The same judge
runs as the board (`serve`, `once`) and in a repository's pipeline
(`check`). Read `README.md` first.

This repository is public and estate-neutral. Every name in a test, an
example, or a doc comes from the fixture world:
https://github.com/orchard-suite/hedge/blob/main/docs/fixtures.md.

Decision records are the registry `TRL` in a Vantage hub, cited as `TRL-5001`.
`vantage.yaml` names the registry; each estate maps this repository to its
hub in its own Vantage settings. Code, comments, and docs cite a record and
never restate it.

## Shape

- `cmd/trellis` is the one binary. `main.go` is the commands, `board.go`
  builds the board and the judge from the config file, `check.go` judges a
  checkout, and `judge.go` is `trellis judge` (through the machine door)
  and `trellis replay`.
  One Go module, no CGO, a distroless image; `action.Dockerfile` is the
  pipeline image, with git.
- `internal/estate` is the data: subjects of open kinds, each with named
  facts that are plain JSON data stamped with the source that read them
  and when, links between subjects, misses, gaps, the waivers and the
  waiver policy, levels, and severities. Sources write through a `Writer`;
  `Carry` keeps last pass's facts a source could not read while they are
  younger than its max age. Nothing here knows a kind, a source, or a
  rule by name.
- `internal/pack` reads pack files and an estate's `tuning.yaml` and
  judges one rule against one subject in CEL: scope, skip, needs, unknown,
  check, with the facts it read and how old they are. `internal/judge` runs
  every rule over the estate, sets aside valid waivers, gives findings
  their owner and deadline, and rolls up by the estate's rollup; it is the
  only place a verdict is made, for the board and for `check` alike.
- `internal/sources` is one interface, `Collect(ctx, *estate.Writer)`, and
  the registry: each subpackage registers its type and settings in `init`,
  and `internal/sources/all` imports them for the binary. A source
  measures and never judges; a read that fails for one subject is a
  `Miss`, and a whole place that was not read, such as a list a catalog
  does not have, is a `Gap`, never a failed run. Each subpackage reads one place: `orrery` (the
  catalog adapter: subjects, waivers, and the findings sink), `kubernetes`, `signing`, `github`, `definitions`,
  `telemetry` (declarations, the probes, and the store clients), `keyvault`,
  `azure_graph`, `azure_policy` (Azure Policy's rules, verdicts, and
  exemptions, passed through),
  `command`, `waivers` (a waiver file in git), `graft` (a request desk's
  program file, and the hub actions and runners it names, through the
  catalog adapter's client), and `repo` (a checkout, for `check`). Shaping is a pure
  function beside the reads, so tests feed it fixtures. `internal/azure`
  signs in as the workload identity and reads the management API: Resource
  Graph over subscriptions or management groups, and resources by id, both
  waiting out throttling; every Azure source reads through it. A Resource
  Graph read names the column that keys its rows, is read in that order,
  and is whole or fails: a key that comes back twice, or a result Azure cut
  short, never passes for the estate.
- A source that reads another policy engine may bring its own pack
  (`sources.Brings`): the board makes the pack set each pass from the
  estate's packs and the brought ones, carries a failed source's pack while
  its facts are carried, and refuses one that clashes with a pack or rule
  the estate has. The judge, history, the page, and the sinks read the
  pass's set from the snapshot, never one fixed at start. A brought rule's
  check reads the engine's verdict and nothing else; one engine per
  property, so an estate deletes its own rule once the engine judges it.
- `internal/gate` is the judge of one change handed in: a platform kit
  change statement made into gate subjects (the change, each effect, each
  unknown part), each effect's proof read from Annals by key before the
  rules run, the fences, the strict join of every rule's route, and the
  drafts. `Catalog` indexes the board's estate once a pass, so a judgment
  looks things up by key and never scans. `Replay` judges a gate pack over
  Annals' past changes with proof as it stood then. Gate packs are
  `family: gate`: `pack.LoadGate` reads them, `pack.Load` never does, and
  their rules run in the gate stage alone, never as board findings. The
  base gate pack `gate` always counts and no estate pack replaces it; the
  fences are the judge's code, not a pack's rules. The judge's limit,
  `gate.Limit`, comes from `BenchmarkJudge` at the scale target: rerun it
  and update the number in `README.md` when the judgment changes.
- `packs/` are the built-in packs, embedded by `trellis.go`. A rule names
  the platform standard rule it enforces and its pillar.
  `packs/testdata/<pack>.yaml` gives every rule a failing, a passing, and
  a no-data subject; `trellis_test.go` fails for a rule without them, or
  without an `aim`. A catalog's scorecard reads trellis's verdict and never
  judges a field itself: the sink puts one per tiered pack whose rules read
  each verdict's `checks`, which the judge records beside the tier and
  which agree with it exactly; `deploy/orrery-catalog/` is the model that
  rolls them up.
- `internal/config` reads the one settings file: the estate's positions
  (rollup, deadlines, owners, waiver policy) and the sources in order, as
  nodes the registry reads; `${NAME}` comes from the environment.
- `internal/board` runs the sources, carries what they could not read,
  judges, keeps the latest snapshot, says what changed since the last pass
  as events, and calls the sinks. `internal/server` serves the board: the session port
  trusts the sign-in proxy's headers (`internal/door`) and must be reachable
  from that proxy only; the machine door (`listen.machine`, 8081) takes
  bearer tokens only, never an identity header, and serves the probes with
  no token and `POST /v1/judge` to the callers `machine.callers` names,
  each token compared in constant time; the control port, where `trellis
  refresh` asks for a pass, listens on loopback only (TRL-2001). `internal/web` is
  the page: plain ES modules, no build step; kinds and pack titles come from
  the board's JSON. The server serves assets under `/static/<release>/`, so
  import modules by relative path and never hard-code `/static/`.
- `internal/history` keeps what the board judged over time in PostgreSQL:
  spans of findings and tiers that open and close, and each pack's daily
  totals, keyed by the board's own names. It records changes, never
  copies. `internal/trend` is what a pass carries to the sinks from it:
  each open verdict's and finding's past and each pack's daily share,
  over 90 days. The board records a pass before the sinks see it. A pass
  reads the past of what is open now, one key at a time: what a pass holds
  grows with the estate, never with how much history is kept.
- `internal/obs` is trellis's own telemetry (logs, OTLP, build_info,
  process metrics, instrumented HTTP, and Go's profiles on a port of their
  own when `listen.profile` names one), `internal/relay` the page's browser
  telemetry relay, and `internal/meter` trellis's metrics, whose names are a
  contract with the estate's dashboard and alerts.
- `internal/packtest` holds packs to their fixtures (`testdata/`, one file
  per pack), for the built-in packs' test and `trellis test`. `internal/sinks/webhook` posts each pass; the
  Orrery sink lives with its adapter. `deploy/kubernetes/` is the base any
  estate starts from; keep its example pack passing `trellis test`.
- `internal/openapi` reads an OpenAPI 3 definition into the facts the
  interfaces pack judges, and compares two for breaking changes.

## How to change it

- A new rule is a rule in its pack file plus its three fixtures. A new
  standard is a new pack file, never a new checker. Rules a source brings
  have no fixtures of their own: the source's generator is held to
  fixtures in its `testdata/` instead. A rule never guesses:
  a fact it needs and lacks is no data, never a pass. Which subjects a
  rule counts is a pack scope, named by the rule, never a skip repeated
  in each rule. A rule a team must prepare for gets `from`, not a quiet
  switch-on.
- A new place to read is a new source package that registers itself,
  says the facts it writes, and is imported by `internal/sources/all`;
  document the fact in `README.md`. A source measures and never judges:
  pass and fail belong in a pack. A new kind of subject needs no change
  outside the source that brings it.
- Estate positions (rollup, deadlines, owners, waiver limits) are
  settings; tuning a pack (params, scopes) is the packs folder's
  `tuning.yaml`, so a pipeline reads the same.
- A catalog on another model gets an adapter of its own, beside
  `sources/orrery`; the judge and the packs never see a blueprint.
- Trellis names no Orrery blueprint or field in code. A catalog's
  blueprints and fields are its estate's own choice, so every list trellis
  reads from Orrery, every field it takes, every entity a finding links
  to, and every blueprint it writes comes from configuration. trellis never
  writes itself into a catalog: its `orrery.yaml` is read by the forge
  reader of an estate that reads this repository. What fits the blueprint standard ships as a mapping file
  an estate includes, never as a default in Go.
- A list a mapping names is the estate's word, not the hub's: the source
  asks the hub which lists it has each pass, and the sink links only to
  those. Never offer the hub a relation to a list it was not seen to have,
  and never let one refused blueprint stop a pass being written.
- Metric, label, and stream names are the estate's: they are settings
  (the telemetry source's `labels`), never code.
- A waiver always ends and always has an owner; one without them does not
  count, nor does one longer than the estate allows. Never add an ignore
  list or a way to mute a rule.
- No real names, emails, or hosts of people in fixtures; `example.test` and
  `user@example.com`.
- Never let a header set identity on the machine door, never admit a
  machine door `/v1` route without a caller's token, never log a token,
  never take a token as text in the settings, and never add a route
  to the session port that skips the gate, or let the control port listen
  beyond loopback (TRL-2001). The one exception is the four
  install files in `internal/web/app` (the icons and the manifest), which a
  device fetches without a session; they carry nothing about the estate.
- Logs are JSON lines on stdout. Log both branches of a decision about who
  is let in.
- `VERSION` is the version of record. No em dashes or en dashes in prose.

## Delivery

Agents push to main.

Commit on `main` and land with `make land` (`make land BRANCH=<branch>` from
a worktree): it runs `make check`, then `release-damnit land`. The repository
is public, so CI runs the same check on GitHub's hosted runners
(`test.yml`), on every pull request and before every release. The release
then versions every `feat` and `fix`, tags it, and builds, signs, and attests
`ghcr.io/orchard-suite/trellis:<version>` for `linux/amd64`. An estate picks
up the new tag its own way. Never edit `VERSION`, the manifest, or
`CHANGELOG.md` by hand.

## Checks

```bash
make check
```

Run it before every commit; `make land` runs it again, and so does CI.
It needs Go (see `go.mod`) and git, and it
judges this checkout with the build and interfaces packs. To prove a change
against the real estate, run one pass with `trellis once -config <file>`,
using a config like the one in `README.md`, and `-packs-dir` to try a new
pack on it first.
