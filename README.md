# trellis

One judge over an estate's standards. A standard is a pack of rules, kept
as data. trellis reads what the estate's catalog, cluster, registry, forge,
vault, and telemetry stores already know. It judges every subject in a
rule's scope against every rule of the packs switched on, and gives one
verdict per subject, per pack, per kind, per platform pillar, per owner,
and for the estate. Every finding carries the detail, the queries behind
it, how old its facts are, the rule of the platform standard it enforces,
a one-line fix, who owns it, and when it is due. A waiver sets a finding
aside, with a reason, an owner, and a category, until it ends.

Four positions set it apart from scorecards and policy engines:

- **No data is never a pass.** Every rule answers pass, fail, n/a, or no
  data, and every fact keeps the source that read it and when. A source
  that cannot read leaves its last facts in place while they are younger
  than its `max_age`, marked carried; after that its rules say no data,
  with why. A finding never vanishes because a scanner timed out.
- **A standard is tested code.** Rules are CEL in files, every rule ships a
  failing, a passing, and a no-data fixture, and a rule that reads a fact
  no configured source writes is named at start and on the board.
- **One judge everywhere.** The board, a one-off pass, and a pipeline's
  `check` load the same packs and the same tuning, so they cannot disagree.
- **Findings are work, not a score.** Each open finding has an owner, a
  deadline from the estate's policy, and a history; what changed since the
  last pass goes out as events a sink routes to whoever owns it. A rule can
  be announced before it counts, and a waiver has a category, a limit, and
  a warning before it ends.

The same judge runs in three places with the same packs, so they cannot
disagree: the board on a schedule, `trellis once` by hand, and `trellis
check` in a repository's pipeline.

| Command | What it does |
|---|---|
| `trellis serve -config config.yaml` | The board: every pack the config switches on, judged every `refresh`, at `/` with JSON under `/v1/`, and the open findings written to the catalog. |
| `trellis once -config config.yaml [-packs-dir drafts/]` | One pass, printed. `-packs-dir` tries packs on the real estate before they are switched on. |
| `trellis check [-dir .] [-packs build,interfaces] [-fail-on high] [-waivers file]` | Judges a repository checkout: its tree, the APIs its `orrery.yaml` offers with what changed since the latest release tag, and, given a declaration and the stores, its telemetry. Exits 1 on a finding at or over `-fail-on`. |
| `trellis refresh [-control 127.0.0.1:8082] [-timeout 20m]` | Asks the running board for a pass now, waits for it and its sends, and prints how each source read and each sink took it. Exits 1 when one failed. |
| `trellis test [-packs-dir packs]` | Holds an estate's packs to their fixtures before they are switched on: a failing, a passing, and a no-data case for every rule. |
| `trellis schema` | The JSON Schema of a telemetry declaration. |

## How it is built

```
sources ──> estate ──> judge(packs) ──> evaluation ──> sinks, board, metrics
```

- **Subjects and facts.** A subject is a kind, a key, named facts, and
  links to other subjects. The kind is an open name (`repository`,
  `image`, `api`, or one a source brings), and a fact is plain JSON data a
  source wrote (`repo`, `scan`, `signing`, `forge`, `pods`, `definition`,
  `telemetry`), stamped with the source's name and when it read it.
  Nothing below the sources knows a kind or a source by name.
- **Sources** read one place each and add subjects, facts, links, or
  waivers. They measure; they never judge. Each source package registers
  its own type and settings, so a new source touches nothing else. Which
  run, in what order, and with what settings is the config file.
- **Packs** are YAML files. A rule names the kind it judges, the platform
  standard rule it enforces, its pillar, its scope, the facts it needs,
  when it does not apply, its check in [CEL](https://cel.dev), the words a
  person reads, and the fix. The built-in packs live in `packs/`; an estate
  adds its own in `packs.dir`, and one with a built-in pack's id replaces
  it. The folder's `tuning.yaml` sets the estate's params and scopes. A
  source that reads another policy engine may bring a pack of that
  engine's rules each pass (`azure_policy` does): the board judges with it
  beside the estate's, and its rules pass the engine's verdict through.
- **The judge** answers each rule for each subject: pass, fail, n/a, or no
  data. No data never counts as pass. A subject is red when a critical rule
  fails, amber when another fails, green when every rule with data passes,
  and grey when none had data; one no rule applies to is in no count.
  Packs, kinds, pillars, owners, and the estate roll their subjects up by
  the estate's `rollup`: the worst of them, or, by share, red for any
  critical finding and otherwise by the percent that are green.
- **Sinks** write the result out: the Orrery adapter writes open findings
  to the catalog, which shows and scores them but never judges, and the
  webhook posts each pass with what changed, each change with its owner.

## Packs

| Pack | Judges | Rules |
|---|---|---|
| `security` | the cluster, repositories, images, workloads, hosts, secrets; tiers `baseline` (every critical and high rule) and `hardened` | branch protection and CI on maintained repositories (pushed within `active_days`, or running something), but no CI asked of a private one nothing consumes; open security alerts on live ones; known-exploited, critical, likely-exploited, and fixable CVEs; signatures and SBOM attestations; images tagged `latest` or from an unlisted registry; root, privileged, and host-namespace pods; missing limits and NetworkPolicies; failed cluster policies; silent and unpatched hosts; secrets that never rotate, and (from 7 December 2026) secrets whose content type names no kind from `secret_types` |
| `telemetry` | services with a declaration | the telemetry standard's probes, below |
| `build` | repositories built from: not archived, a fork, or empty | an `AGENTS.md` at the root, no `CLAUDE.md` anywhere, and an `orrery.yaml` beside every app the estate runs from it |
| `delivery` | repositories built from, and the private ones among them; announced until 22 November 2026 | a private repository's pipeline builds no desktop or mobile app (a macOS, Windows, or iOS runner, an app build step, or an app-shipping action), and runs only when something consumes it: the estate runs what it builds, it is versioned, or it deploys a site. A repository that builds something has a root Makefile, a target named for each app's platform (`mac`, `ios`, `android`, `windows`), and a default target that builds: the first platform in `platform_order` it has, or a target that runs it, and otherwise not a check or tidy (`not_builds`) |
| `catalog` | the apps and programs a catalog holds, but not one planned or retired; tiers `described`, `owned`, and `operable` | an app has a description and a repository; an owner, a tier and a lifecycle, and something discovered that runs it; a runbook, documentation, and operator notes. A program has a description, a team, and something in it |
| `access` | an Orrery hub's own access rules, read with `posture: true`, and the grants and applications a catalog holds; tiers `hub`, `hygiene`, and `reviewed`, the last two judged once named in `access_tiers` | `hub`: blueprints reviewed for access within `review_days` of landing, and again when fields come after a review; no field named like money (`money_fields`) left open; no field nobody but an admin sees; every audience lets someone in and names only people the hub knows; no access token holding the admin role. `hygiene`: no grant to an account that is gone or disabled; no role in `admin_roles` held by a user directly, only through a group (n/a while the list is empty); a guest's grant flagged. `reviewed`: an application someone holds a grant to has an access review within `access_review_days` (365) |
| `alerts` | every alerting rule a Prometheus has loaded, read by the `alertrules` source; tier `sound` | every selector the rule needs selects something that exists: its metric had a series in the lookback, and no matcher on a label in `place_labels` (where a series comes from, such as `job` or `namespace`) selects nothing of it, while a filter on another label, such as `outcome="failed"`, may simply not have happened, and a selector on the right of `unless` only takes away; its alerts carry one of `workload_labels`, from the rule's own labels or the labels its query keeps; and a wait of `long_wait_seconds` (12 hours) or more is not on alerts that may carry a `moving_labels` label (`node`, `pod`), which changes at every move and restarts the wait |
| `graft` | a request desk's program file, the hub action each of its lanes is, and the hub runner each runner name is, read by the `graft` source; tiers `declared` and `granted` | `declared`, from the program file alone: no lane for outside reporters ships a small fix or passes a protected path; the production and nonproduction runner of one name are two runners; every repository protects a path beyond `graft.yaml`; `limits.small_fix_lines` is at most `max_small_fix_lines` (400) and `limits.spend_usd` at most `max_spend_usd` (25); `trusted` is off, or waived on purpose; `hours.schedule` is set when a lane lands without review. `granted`, from the hub: every lane has its action; no lane action is open to everyone, or has a rule that names no people and no ownership; every lane action has an access rule; no lane carries the `data` tier; the lowest lane and an outside lane carry no tier; no rule of a protected lane lets a plan through with `approval: false`; every runner exists with a `serves` rule, whose `on` is set, whose `sources` name a source (one of `desk_sources` when set), and whose `tiers` are not empty; a runner that serves `read` does not serve `act`; no runner serves `data` unless `data_runners` is on |
| `interfaces` | the HTTP APIs services built here offer; tiers `defined` and `shaped` | a definition named in the catalog and readable, routes under a major version, a declared sign-in on every operation, one documented error shape, a unique `operationId` and a summary on every operation; in `check`, no change that breaks a caller since the latest release |

### A rule

```yaml
- id: image.cve-kev
  title: Runs a known-exploited CVE
  aim: Runs no known-exploited CVE           # optional: the requirement as met
  kind: image
  severity: critical
  standard: vulnerability-exposure-in-the-catalog-count-everything-make-
  pillar: platform/security
  scope: running_image                       # optional: one of the pack's scopes
  skip: [{ if: ..., because: ... }]          # n/a when one holds
  needs: { scan.cve_kev: the image was not scanned }   # no data without it
  unknown: [{ if: ..., explain: '"..."' }]   # no data when one holds
  check: scan.cve_kev == 0                   # passes when true
  detail: plural(scan.cve_kev, "CVE", "CVEs") + " in CISA's KEV catalog"
  evidence: ...                              # optional: a list of {source, query, got}
  fix: Rebuild on a base image that fixes the CVE.
  stages: [board, check]                     # optional; both by default
  status: draft                              # optional: tried, never counted
  from: 2026-12-01T00:00:00Z                 # optional: announced until then
  controls: ["soc2:CC6.1", "iso27001:A.8.9"] # optional: outside frameworks' controls it covers
  tier: baseline                             # optional: one of the pack's tiers
```

A pack file holds `pack`, `title`, `about`, `version`, `family`
(`security` or `platform`, which sets the catalog list its findings go
to), `status` (`active`, or `draft`: judged and shown, never counted,
written, or alerted on), `params` (defaults the estate overrides), `scopes` (named CEL filters a
rule holds in), `tiers` (its levels, lowest first, such as `[baseline,
hardened]`; every rule then names one), and `rules`.

A rule's `aim` is the requirement as met, in positive words, for a check
mark beside it: the title "Virtual machine has no Azure Backup" has the aim
"Virtual machine has Azure Backup". It changes nothing that is judged.
Every built-in rule has one; an estate's own pack may leave it out, and a
consumer then shows the `title`.

A scope is defined once and named by the rules it bounds, so a pass rate
divides only by what the standard means to judge: a subject outside a
rule's scope is n/a and in no count. A scope reads facts like a check; one
it cannot read leaves the rule at no data, never in or out. An estate
redefines a scope in its `tuning.yaml`.

A rule's `status: draft` tries it on the real estate without counting it;
a rule with `from` is announced until that day: judged and shown with the
day it counts from, in no level, written to no catalog, and a warning in a
pipeline. A draft pack makes every rule in it a draft. A
subject reaches a tier when no rule of that tier or a lower one fails or
lacks data, and the board counts each pack's subjects by the tier they
reached. A finding carries its rule's controls and tier into the catalog,
so a scorecard there can level by tier and a report can group by control. Expressions see each fact by name, `subject`, `params`, and `now`,
with CEL's string, list, set, and optional-field extensions
(`azure.?tags.?env.orValue("")`), `plural(n, one, many)`, and
`days_since(time, now)`. `linked` holds the facts of every subject linked
to this one, by kind, either way: `linked.workload[0].workload.namespace`.
trellis reads each rule's expressions for the facts they name, so every
answer carries `as_of`, when the oldest of them was read, and whether one
was carried. `packs/testdata/` gives every built-in rule a
failing, a passing, and a no-data subject, one file per pack, and a test
holds every rule to them. An estate does the same for its own packs with
`trellis test`, which reads every file in `testdata/` beside them and
judges with the folder's tuning. A case's `linked` key holds the subjects
linked to it, by kind. `params`, by pack, lay values over the tuning for
every case, such as a list a rule is n/a without:

```yaml
now: 2026-11-04T00:00:00Z
params:
  access: { access_tiers: [hygiene, reviewed], admin_roles: [Admin] }
rules:
  storage_account.tls:
    fail: { azure: { minimumTlsVersion: TLS1_0 } }
    pass: { azure: { minimumTlsVersion: TLS1_2 } }
    nodata: { azure: {} }
```

### The access pack

The `hub` tier judges an Orrery hub's own access rules and is always on.
The `hygiene` tier judges each grant a catalog holds (`access_grant`), and
the `reviewed` tier each application someone holds a grant to, or one
marked `access_review` for a system no pusher reads yet (`application`); the standard mappings read both from Entra's lists and
`it_access_review`. Both wait until the estate names them in its
`tuning.yaml`, with its admin roles in each system's own words:

```yaml
params:
  access:
    access_tiers: [hygiene, reviewed]   # either, or both
    admin_roles: [Admin, Owner]         # empty: the admin rule is n/a
    access_review_days: 365             # the default
```

A system with no grants in the catalog gives these rules nothing to judge.
An application whose reviews cannot be read says no data, never pass.

### The graft pack

A request desk takes a person's report and lets agents plan, build, and
land the change. Its safety is configuration in three places, and the
`graft` source reads all three: the desk's program file, the hub action
each lane is (`graft_<program>_<lane>`), and the `serves` rule of each
hub runner the file names. The `declared` tier judges the file alone. The
`granted` tier judges what the hub grants, and says no data, never pass,
while the hub cannot be read.

```yaml
params:
  graft:
    max_small_fix_lines: 400      # the default
    max_spend_usd: 25             # the default
    data_runners: false           # true: a runner may serve the data tier
    desk_sources: [desk-tools]    # the desk's own hub source; empty: any named source passes
```

A program marked `trusted` is a low finding that says what it means: the
sandbox holds a read-only cloud key that reads both environment tiers. An
estate that chose it waives the finding, with an owner and an end. Which
lanes land through review and which may change a protected path are facts
on the program (`review_lanes`, `protected_lanes`), not findings: a program
with no protected lane changes no protected path through the desk.

### The interfaces pack

Each API a service built here offers names its machine-readable definition
with `spec` in the service's `orrery.yaml`: a path from the repository's
root, or a full URL. The definition lives with the code, generated from it with a test
that fails while it is stale. trellis holds no copy: the `definitions`
source reads it from its link every pass, and `check` reads it from the
checkout and compares it with the same file at the latest release tag. A
breaking change is an operation gone, a parameter or a body now required, a
body field now required, or a field a success response no longer returns.
Retiring a whole major version once a newer one ships beside it is not one.
An MCP server and a web page are n/a, and a definition in another language
than OpenAPI is no data for the shape rules.

### The telemetry pack

One short declaration per service says what it is:

```yaml
service: myapp              # also OTEL_SERVICE_NAME and the Prometheus job
title: MyApp
namespace: myapps
app: myapp                  # the pods' app label, when not named after the service
container: myapp            # when not named after the app label
host: myapp.example.org
routes: [/healthz, "/api/items/{id}"]
page: true                  # a page that reports browser telemetry as myapp-web
runtime: node               # go | node | rust | python
edge: both                  # traefik | cloudflare | both
public: false               # a public page reports one view per load
max_label_values: 50
catalog_gauges: [myapp_field_value]  # one series per catalog value, on purpose
```

The `telemetry` source measures what the stores hold for each declared
service and writes the measurements, with the queries behind them, as the
service's `telemetry` fact; the pack's rules judge them. A store that
cannot be read for one measurement leaves it with an `error`, which its
rule reads as no data; when no store answers at all, the source misses
the fact and the board carries the last one.
How the estate's stores name things is the source's `labels` setting:
`job`, `logs`, and `browser` default to OpenTelemetry's and the Kubernetes
log collector's usual names, and fill `{service}`, `{namespace}`, `{app}`,
`{container}`, `{host}`, `{host_dashed}`, and `{web}` from the declaration; the edge's access log (`edge_access`,
`edge_path`, `edge_method`, `edge_status`, `browser_agent`), its router
counter (`router`), and the public view beacon (`view_beacon`) are the
estate's own, and the probes that need them answer that the edge is not
configured until they are set.

| Measurement | What it holds | The rule passes when |
|---|---|---|
| `build_info` | series, versions, unversioned | `<service>_build_info` is there with a `version` label. |
| `http_duration` | reported, routes, unlabelled, declared, edge_hits | Durations carry `http_route`, and every declared route the edge served in the window appears. |
| `process_cpu`, `process_memory` | series and the states or kinds seen | The process's CPU time by state and memory by kind are reported. |
| `event_loop` | the series missing | Node only: event loop delay p99 and max are there. |
| `logs`, `trace_ids` | a sample of recent lines and what each lacks | Lines are JSON with `timestamp`, `level`, and `msg`, and carry `trace_id`. |
| `cardinality` | the widest labels, and apart from them the labels of the `catalog_gauges` | No label has more than `max_label_values` values, on any metric but the catalog gauges, which the detail and the evidence name. |
| `edge_router` | configured, routers | Traefik edge: a router serves the host. |
| `page_loads`, `edge_views` | configured, loads, views | Public only: view reports reached the edge. |
| `browser_logs`, `browser_trace` | lines, joined traces | Page only: browser lines exist, and a trace spans browser and server. |

The browser and view rules are n/a in a window when no browser loaded a
page of the host through the edge.

## Sources

| Type | Reads | Writes |
|---|---|---|
| `orrery` | An Orrery hub, through the estate's mappings | whatever its mappings make: subjects of any kind with the facts they compute, and the waivers |
| `kubernetes` | The cluster API, read-only, and the policy engine's PolicyReports | the cluster (`cluster`) and each workload's pods (`workload`, `pods`) |
| `signing` | The registry, through sigstore | each image's `signing`: a keyless signature by the trusted identity, and an SBOM attestation |
| `github` | Each repository on GitHub: its visibility, its tree on the default branch, and its build files, each fetched once by its hash | `forge` (`private`, `empty`, `files`) and `build` |
| `definitions` | Each API's definition at its link | `definition` |
| `telemetry` | Declarations, then Prometheus, Loki, and Tempo | services with `telemetry` |
| `alertrules` | A Prometheus's loaded alerting rules (`prometheus`), and for each series selector their queries use, its series over `lookback` (7 days) | one `alert_rule` subject per rule with `alert`: its name, group, file, query, wait (`for_seconds`) and own labels; each selector with its series, whether it is `optional` (only on the right of `unless`), and when it selected nothing, its metric's series and each matcher's alone; and the labels every alert it raises carries (`keeps`) and that some may carry (`may_keep`), worked out from the query and the labels each selector's series carry |
| `graft` | A request desk's program file, at a path or an address (`program_file`), and the catalog hub (`hub`, `token`) for the lanes and runners it names. The token needs `posture.read`, which lists every action, and must see the blueprint the lane actions run on | the program (`graft_program`: `trusted`, its lanes by flag, `small_fix_lines`, `spend_usd`, `hours_schedule`, each as the file says and zero or empty when left out), each repository (`graft_repository`: its `protected` paths beyond `graft.yaml`), each lane (`graft_lane` from the file, and `lane_action` from the hub: whether it `exists`, `everyone`, the count of `grants` and `audiences`, `home`, `approval`, each rule's `who`, `owns`, `where`, and `approval` as `none`, `other`, or `self`, and its `tiers`), and each runner a name has in a tier (`graft_runner`: its `id` and its `twin` in the other tier, and `runner_source` from the hub: whether it `exists` and `serves`, with the rule's `tiers`, `sources`, and `on`) |
| `keyvault` | An Azure Key Vault, as the pod's workload identity, which needs Key Vault Reader: names and dates, never a value | secrets with `secret`: when each last changed, its content type, and that type's kind (`type`, the words before a colon) |
| `azure_graph` | Azure Resource Graph queries, as the pod's workload identity, over `subscriptions` or `management_groups` | each query's rows as subjects of its kind, with the row as a fact (`azure` unless named) |
| `azure_policy` | Azure Policy, as the pod's workload identity: its compliance states, assignments, and exemptions from Resource Graph over `management_groups`, and its definitions and initiatives by id | a pack of Azure's rules, each resource's verdicts (`azure_policy`) on the subject of its `kind` (`azure_resource`), and Azure's exemptions as waivers. Below |
| `command` | A program the estate runs, printing `{"kind", "key", "title", "fact", "value"}` per line | whatever it prints |
| `waivers` | A waiver file kept in git | waivers, beside any the catalog holds |

The `build` fact is what a repository's build files say, shaped by
`internal/buildfiles` for `github` and for `check` alike: `make` (the root
Makefile's `present`, `default` goal, explicit `targets`, and `deps`, each
target's prerequisites and the targets its recipe runs with `$(MAKE)`),
`workflows` (each file under `.github/workflows` with every job's
`runs_on`, a matrix's values included, its steps' `runs`, and the actions
it `uses`), and `apps` (each app a project file builds, by `platform`:
`ios` and `mac` from an Xcode project's `SDKROOT`, `android` from a Gradle
file that applies `com.android.application`, `mac` from a `package.json`
that depends on Electron or a `tauri.conf.json`, `windows` from a
`.csproj` that targets Windows).

Every source takes `name` (its name on the board and the stamp on its
facts; its type unless set, and two sources may not share one) and
`max_age` (how long the board carries its facts when it cannot read them;
24h unless set). A source that fails outright has every fact carried; one
that fails for a single subject records a miss, with why, and that fact
alone is carried. One that cannot read a whole place, such as a list its
catalog does not have, records a gap: the fact is a miss on every subject
of the kind, with why, its last readings are carried, and the board names
the gap beside the source. A gap is never a failed run, so everything else
the source read is judged as usual. A fact older than its max age is
dropped and the rules that need it say no data, naming the source and the
age.

With `posture: true`, the `orrery` source also reads the hub's access
posture, which its token needs the `posture.read` permission for: one
`access_hub` subject for the hub (`posture`), one `access_blueprint` for each
blueprint (`access`: when it came and was last reviewed, the fields since,
the fields open to whoever sees it, and the fields nobody but an admin
sees), and one `access_audience` for each audience (`audience`). The hub
keeps the rules and shows the facts; the `access` pack judges them. When the
posture cannot be read, those subjects are not there and the rest of the
pass still counts.

The `graft` source reads one program:

```yaml
- type: graft
  program_file: graft/program.yaml    # beside the settings file, or an https address
  program_file_token: ${PROGRAM_FILE_TOKEN}   # a bearer token, for an address that asks one
  hub: https://orrery.example.org
  token: ${ORRERY_TOKEN}
```

A program file that cannot be read fails the run, and the board carries
the last reading. A hub that cannot be read, or an action the hub lists
but the token may not read, is a miss on that lane's `lane_action` or that
runner's `runner_source` alone, so the file's rules still count. A lane
action the hub's posture does not list is missing, not unknown.

`azure_graph` does the same for a cloud estate's resources or security
recommendations, one Resource Graph query each. It reads the subscriptions
named, or the `management_groups` named, or everything the identity may
read. A query's `key` column (`id` unless named) is unique per row: the
rows are read in its order, a thousand at a time, and a read in which a key
comes back twice fails, because Resource Graph pages an unordered result by
position and so repeats some rows and leaves others out. Azure Policy's
verdicts come through `azure_policy`, never a query here.

### Azure Policy

Where a native policy engine judges a property, trellis does not judge it
again. Azure's rules live in Azure Policy, assigned at management groups,
and every exception is an exemption made in Azure with its reason. The
`azure_policy` source reads them and passes them through, so the board and
the catalog show Azure's rules, verdicts, and exemptions in one list beside
the estate's own. An estate deletes its own rule for a property once
Azure Policy judges it: one engine per property.

```yaml
- type: azure_policy
  max_age: 48h                     # Azure re-evaluates about once a day
  management_groups: [<root management group id>]
  kind: azure_resource             # the kind the catalog's Azure resources are mapped as
  tiers: [baseline, hardened]
  tuning:
    default: { severity: medium, tier: baseline }
    categories:                    # by a definition's policy category
      Security Center: { severity: high }
    definitions:                   # by definition name, a GUID for a built-in one
      <definition name>: { severity: critical, tier: hardened }
  exemptions:                      # the exemption metadata keys the estate writes
    owner: owner
    control: control
  frameworks:                      # by initiative name: whose controls its groups are
    <initiative name>: <framework>
```

Each pass it reads, over the management groups named:

- **Compliance states**, assignments, and exemptions from Resource Graph's
  `policyresources` table, page by page, waiting out its throttling. A
  query scoped to management groups sees what is assigned and exempted at
  a group; one scoped to subscriptions sees only theirs. Resource Graph
  holds no definitions.
- **Definitions and initiatives** from the management API by id, each
  read again after a day.

What it makes of them:

- **Rules.** One per policy definition, with each assigned initiative
  flattened into its members: `azure.<definition name>`, lower case. The id
  follows the definition, not an assignment, so moving an assignment keeps
  it; a definition assigned twice is one rule, and the worst state wins. A
  member Azure has not judged yet is still a rule. The title is the
  definition's display name, the fix its description. An initiative's
  compliance groups are the rule's `controls`, as `<framework>:<group>`,
  where the framework is the one `frameworks` names, or the initiative's
  name. Severity and tier are the estate's: `tuning.default`, laid over by
  the definition's policy category, laid over by its name. The rules run
  on the board only; a pipeline's check never reads Azure.
- **Verdicts.** Each resource's subject, keyed by its lower-case resource
  id, gets the `azure_policy` fact: per rule, Azure's `state`, the
  `assignments` that decided it, and the `exemptions` that cover it. A rule
  passes on `Compliant`, and on `Exempt` when the resource meets the
  policy anyway; fails on `NonCompliant`, and on `Exempt`, where the
  waiver below may cover it; and has no data on anything else Azure says,
  such as `Error`. A subject of the kind that an earlier source brought and
  no policy judges is outside every rule, so list `azure_policy` after the
  catalog source.
- **Waivers.** A state never names its exemption, so the source finds it:
  made on the state's assignment, at a scope above the resource (a
  management group the resource is under, or a path that holds it), for
  the state's initiative member or every member, and with any resource
  selector holding. The exemption's description, or its display name, is
  the reason. Azure's `Waiver` is `accepted`, ending at `expiresOn`;
  `Mitigated` is `mitigated`, reviewed at `expiresOn`, with the control
  under the `control` metadata key. The owner is under the `owner` key.
  Each waiver keeps the exemption's id. A failure Azure exempts under
  several assignments needs an exemption for each, and they make one
  waiver that counts only if each would. A failure some other assignment
  finds non-compliant gets no waiver at all.

Azure allows an exemption with no end and no owner; trellis does not count
it. The board shows it with why, and the finding stays open until the
exemption gains both. The reader needs Reader at each management group
named.

### Reading a catalog

trellis names no Orrery blueprint or field. A catalog's blueprints and
fields are its estate's own words, so the `orrery` source reads only what
its `mappings` name. A mapping makes the entities of one blueprint into
subjects of a kind the packs judge, with a fact computed by
[CEL](https://cel.dev) expressions:

```yaml
- type: orrery
  url: https://orrery.example.org
  token: ${ORRERY_TOKEN}
  include: [standard]              # the mappings for a catalog on the blueprint standard
  mappings:
    - kind: storage_account        # the kind a pack judges
      blueprint: azure_storage     # the estate's list
      where: entity.fields.type == "storage_account"
      key: entity.fields.resource_id.lowerAscii()   # so azure_graph's facts land on the same subject
      fact: azure
      join:
        programs: { blueprint: program }
      fields:
        tls: entity.fields.minimal_tls
        shared_key: entity.fields.shared_key_access
        criticality: joins.programs[entity.relations.program[0].key].fields.criticality
```

| Setting | What it does |
|---|---|
| `kind`, `blueprint` | Required: the subjects' kind, and the list they come from |
| `where` | Keeps the entities for which it is true |
| `each` | One subject per element of a list, read as `item` |
| `key`, `title` | Key and title each subject; the entity's when unset |
| `fact` | The fact's name; the kind when unset |
| `fields` | The fact's fields, one expression each. Unset writes every field of the entity and its `relations` |
| `join` | Other lists an expression looks an entity up in, under `joins.<name>`, keyed by `key` (an expression over that list's `entity`; its key when unset), a list per key with `many: true` |
| `optional` | A list the hub has that cannot be read is a gap instead of failing the read |
| `links` | Ties each subject to others, a CEL list of `{kind, key}`, such as the workload an image runs in |
| `link` | A finding on the subject links to the entity; on unless `false` |
| `ref` | A finding on the subject links to another entity instead: `{blueprint, key}`, the key an expression, such as the scan of an image a workload runs |

An expression sees `entity` (`key`, `title`, `blueprint`, `fields`, and
`relations`, each a list of `{blueprint, key, title}`), `item`, and
`joins`, with CEL's optional fields (`entity.fields.?x.orValue(null)`), its
string, list, and set extensions, and three functions: `image(ref)` takes an
image reference apart into `ref`, `registry`, `tag`, and `pinned`;
`repo_url(url)` gives a forge URL's `base` and `folder`; `sum(list)` adds a
list's numbers, null when there are none. A field whose expression fails,
or reads what the entity lacks, is unknown, so a rule that needs it says no
data; one whose key cannot be computed is skipped and logged. A field the
catalog holds no value for is absent, so `entity.fields.?runbook.orValue("")`
is empty for a runbook nobody set, and a rule fails it. Two mappings
may write the same subject and fact; their fields add up.

Each pass the source first asks the hub which lists it has. A list a
mapping names that the hub does not have, or that the token may not read,
is a gap, never a failed read: the mapping writes nothing, its fact is a
miss on every subject of its kind, with the reason, what it read last is
carried while young enough, and every other list is read, judged, and
written as usual. So a list that arrives after the mapping that names it,
or a mistyped name, costs only its own subjects, and the board names it
beside the source. A list the hub has that cannot be read fails the whole
read, so a partial read never judges less than is there; with `optional`
it is a gap too. A mapping that joins a list that was not read is a gap
the same way.

The source asks the hub for only the fields and relations the mappings
read of each list, and of each list they join. An expression reads one by
name: `x.fields.name`, `x.fields.?name`, or `x.fields["name"]`, on the
entity or on any entity it reaches. A mapping with no `fields`, or an
expression that takes `entity`, its `fields`, or its `relations` whole,
asks for every one. The source keeps each page it read with the hub's
tag and sends the tag back on the next pass; a page nothing has moved on
is answered `304` and read from the copy kept. The copies live in the
process, so a restart reads every page once in full.

Quote an expression that holds `: `, ` ? `, `{`, or `[` at its start, or
write it as a `>-` block, so YAML reads it as one string.

A mapping of kind `waiver` makes waivers: its fields are `rule`, `subject`,
`reason`, `owner`, `category`, `control`, `match`, `granted_at`,
`expires_at`, and `review_at`.

`include` adds the mappings of files, each a `mappings:` list: `standard` is
the built-in [`standard.yaml`](internal/sources/orrery/standard.yaml) for a
catalog on the blueprint standard, which gives the built-in packs their
facts (`repo` with `last_push`, `runs` with the folders and the `owners`
of what runs from a repository, `workload`, `image` linked to its
workloads, `scan` linked to the image's entity, `host`, `api`, `app` from
`sw_workload`, `program`, `grant` on an `access_grant` from each
`entra_app_role_assignment` with its `role`, `principal_type`, `app`,
`account`, `holder` (`active`, `disabled`, or `gone`, through the
`entra_user` and `entra_group` lists), and the user's `user_type`, and
`application` from `it_application` with its `grants`, counted through
its `sign_in` Entra apps, and the `reviewed_at` of its latest
`it_access_review`, empty when none) and reads `plat_waiver`; any other is
a path relative to the config file.

The `command` source is how an estate brings a scanner trellis has no
source for: the program reads its tool, and a pack of the estate's reads
the fact. A source that fails leaves the rules that needed it at no data;
the rest still count.

## Waivers

A waiver names a `rule`, a `subject` (both as the board shows them), a
`reason`, an owner, and a `category`, and it ends. An `accepted` risk ends
on `expires_at`: debt the owner takes on. A `mitigated` one names the
`control` that meets the rule's intent another way and ends at
`review_at`. With no category, a waiver that names a control is mitigated
and any other is accepted. One that lacks what its category needs, names a
rule no pack switched on has, or runs longer than the estate's
`waivers.max_days` for its rule's severity (counted from `granted_at`, or
from now when it says none) does not count, and the board lists it with
why. Within `waivers.warn_days` of its end it is ending: the board shows
it and a sink gets an event. The Orrery adapter reads waivers through a
`waiver` mapping, from whatever list the estate keeps them in, and the
`azure_policy` source makes Azure's exemptions waivers. The board
counts the waivers on each rule: a pile on one rule says the rule is
wrong. Git, where waivers are kept, is their history.

A waiver file holds the same waivers for an estate whose catalog keeps
none (the `waivers` source), and for `trellis check`, which reads
`trellis.waivers.yaml` in the app's folder, or the file `-waivers` names.
That is how a pipeline accepts a change the gate flags on purpose, such as
a definition that now documents a body its route always required:

```yaml
waivers:
  - rule: api.breaking
    subject: github/example/app/v1
    reason: The definition now documents the body the route always required.
    owner: the API's owner
    category: accepted
    granted_at: 2026-11-04T00:00:00Z
    expires_at: 2026-12-01T00:00:00Z
    match: POST /v1/things now requires a body
```

`match` narrows a waiver to the findings whose detail holds that text. A
waiver without it covers every finding of its rule on its subject, so a
gate waiver should name the change it accepts: then a different break on
the same API still fails the gate.

## Configuration

One file, read from `-config` or `TRELLIS_CONFIG`. `${NAME}` is read from
the environment, so secrets never sit in it.

```yaml
estate: acme
public_url: https://trellis.example.org
refresh: 15m
sign_in:
  mode: headers              # headers: trust a sign-in proxy's headers; open: a laptop
  email_header: X-Forwarded-Email
  groups_header: X-Forwarded-Groups
  roles: [Viewer]            # empty admits anyone the proxy signed in
packs:
  dir: /etc/trellis/packs    # the estate's own packs and its tuning.yaml
  on: [security, telemetry, build, interfaces, catalog]
  rollup: { security: { mode: worst } }   # a pack's own rollup
rollup: { mode: share, green: 95, amber: 80 }  # or worst, the default
deadlines: { critical: 7d, high: 30d, medium: 90d, low: 180d }
owners:                      # CEL per kind over the facts and linked; "*" for the rest
  repository: 'runs.owners[0]'
  image: 'linked.workload[0].workload.namespace'
  app: app.owner
waivers:
  max_days: { critical: 30, high: 90, medium: 180, low: 365 }
  warn_days: 14              # 14 unless set
sources:
  - { type: orrery, url: "https://orrery.example.org", token: "${ORRERY_TOKEN}", include: [standard] }
  - { type: kubernetes, cluster: cluster-a }
  - { type: signing, subjects: '^ghcr\.io/example/', identity: '^https://github.com/example/', issuer: https://token.actions.githubusercontent.com }
  - { type: github, owners: [example], token: "${GITHUB_TOKEN}", max_age: 6h }
  - { type: definitions, github_token: "${GITHUB_TOKEN}" }
  - type: telemetry
    declarations: /etc/trellis/declarations
    prometheus: http://prometheus:9090
    loki: http://loki:3100
    tempo: http://tempo:3200
    catalog: sw_workload
    labels: { edge_access: '{app="edge", site="{host}"}', edge_path: path }
sinks:
  orrery:
    url: "https://orrery.example.org"
    token: "${ORRERY_PUSH_TOKEN}"
    write: { finding_security: sec_finding, finding_platform: plat_finding, verdict: plat_verdict, pack: plat_pack, pillar: plat_pillar }
    scorecards: { field: "trellis_{pack}_checks" }   # optional: a scorecard per tiered pack
browser_telemetry: { url: "${FARO_URL}", key: "${FARO_API_KEY}" }
```

The estate's tuning sits beside its packs, so the board and every
pipeline read the same file:

```yaml
# /etc/trellis/packs/tuning.yaml
params:
  security: { allowed_registries: [ghcr.io, docker.io], active_days: 90 }
scopes:
  security:
    # Branch protection and CI only where something runs.
    maintained_repo: '!repo.archived && !repo.fork && size(runs.folders) > 0'
```

The rollup is a position the estate takes. Worst suits a small estate
where every subject matters; share suits a large one, where one stale
corner should not hold every group red, while a critical finding still
does. Subjects keep their own worst-of level either way.

The session port trusts the sign-in proxy's headers, so the network must
let only that proxy reach it. The health port answers `/healthz` and
`/readyz` and trusts nothing.

The control port, `listen.control`, is `127.0.0.1:8082` unless set, and
trellis refuses any address that is not loopback. It answers one route,
`POST /v1/refresh`, which runs a pass and answers when the pass and its
sends are done. Whoever may exec into the pod, or forward a port to it,
may ask; nobody else can reach it. After a change the board should see
now, such as a field added to a catalog blueprint, ask for a pass instead
of restarting the board or waiting for the next one:

```bash
kubectl -n trellis exec deploy/trellis -- /app/trellis refresh
```

It prints each source's outcome and each sink's, `orrery` and `webhook`,
with what the sink could not deliver: a write the hub failed, entities it
rejected, or a scorecard it refused. It exits 1 when a source or a sink
failed. A pass asked for while one runs is the pass right after it, shared
by every ask until it starts; the schedule's own pass skips while one
runs. `GET /v1/board` carries no sends; the pass answer and the logs do.

In a container, the board keeps Go's heap to 85% of the container's
memory limit (cgroup v2 `memory.max`), so the collector works harder
before the kernel would kill it. A `GOMEMLIMIT` set in the environment
wins. Give the container room for the estate's live set: a few hundred MB
for some thousands of subjects judged against a few hundred policy rules.

`listen.profile` names a third address, off unless set, that serves Go's
profiles of the process under `/debug/pprof/`, for the day the board needs
more memory or time than its estate explains. A profile holds whatever the
process holds, so bind it to the pod alone and reach it by port-forward:

```yaml
listen: { profile: "127.0.0.1:6060" }
```

```bash
kubectl port-forward deploy/trellis 6060 &
go tool pprof -top http://127.0.0.1:6060/debug/pprof/heap
```

## In a repository's pipeline

```yaml
- uses: actions/checkout@v7
  with: { fetch-depth: 0 }      # the release tags, for comparing definitions
- uses: orchard-suite/trellis@v4.0.0       # pin the release's commit SHA in production
  with:
    packs: build,interfaces
    match: myapp-v*             # in a repository of several packages
    fail-on: high
```

Or run the binary: `trellis check -packs interfaces -match 'myapp-v*'`.
`-packs-dir` names the estate's packs folder, so the check reads the same
packs and `tuning.yaml` the board does. An open finding at or over
`-fail-on` fails the job. An announced rule's finding prints as `soon`
with the day it counts from, and never fails it. A counted rule with no
data prints with why; `-unchecked fail` fails the job on one.

## The board

`/` has six views. Overview gives the verdict by the estate's rollup and
the share conformant, the counts of open, overdue, unchecked, waived,
announced, and draft findings, each pack, what changed since the last
pass, the owners with the most open and overdue, each pillar from live
state, and each source with what it missed and carried and each place it
could not read, with why. Findings lists
them by state, owner, and deadline, marking those judged on carried facts.
Subjects lists everything checked; a subject opens with every fact, the
source that read it, and when. Owners lists who answers for what. Rules
shows each rule's health: how it answered across the estate, and which
ones mostly lack data, read facts nothing writes, or draw waivers.
Waivers lists each exception by status, with what it covers.

- `GET /v1/board`: the estate's tally and rollup, each pack, kind, pillar,
  and owner, every rule with its answers, each source's run, the waivers
  with their status, and the last pass's events.
- `GET /v1/findings?pack=&kind=&severity=&rule=&pillar=&control=&owner=&state=&overdue=true`:
  findings in every state and every subject with its level.
- `GET /v1/subject?kind=&key=`: one subject with its findings, unchecked
  rules, and every fact with its source and when it was read: what a
  person or an agent needs to fix it, in one answer.
- `POST /v1/refresh`: run a pass now (same origin only). It answers at
  once; the page loads the pass when it is done.
- `GET /apple-touch-icon.png`, `/icon-192.png`, `/icon-512.png`, and
  `/manifest.webmanifest`: what a device reads to add the board to a Home
  Screen or install it as a desktop app. A device fetches them without a
  session, so they are the only routes on the session port that skip the
  sign-in check. Their source is `internal/web/app/app-icon.svg`, rendered
  with `rsvg-convert -w N -h N`.

Each pass's events say what changed since the one before: a finding
`opened`, `resolved`, `waived`, gone `overdue`, or `unknown` (it had no
data, so nobody can say it was fixed), and a waiver `waiver_ending` or
`waiver_ended`, each with its owner.

## Findings in the catalog

The Orrery sink writes into the blueprints its `write` settings name, so an
estate keeps its own words:

```yaml
sinks:
  orrery:
    url: https://orrery.example.org
    token: ${ORRERY_PUSH_TOKEN}
    write:
      finding_security: sec_finding   # the security family's findings
      finding_platform: plat_finding  # every other family's
      verdict: plat_verdict           # one per subject and tiered pack
      pack: plat_pack                 # the packs the board judges with
      pillar: plat_pillar             # the list a finding's pillar is in; unset links none
      rule: plat_rule                 # every rule judged with; unset writes none
      waiver: plat_weighed_waiver     # every waiver weighed; unset writes none
```

A role left unset is written under its own id from
[`blueprints.yaml`](blueprints.yaml): `finding_security`,
`finding_platform`, `verdict`, `pack`; `pillar`, `rule`, and `waiver`
are written only when named. The sink offers those blueprints
on its first pass, and again on a pass that finds a new list to link to,
then writes each pass's open findings and verdicts as one snapshot
under scope `trellis`, so a finding that clears, or that a waiver now
covers, leaves the hub. Where the hub takes sightings, each batch names
its entities by a digest of what it sends first, and sends in full only
the ones the hub asks for, so a pass costs what changed. A finding judged on carried facts stays, marked
`carried`, instead of leaving and coming back. A finding is keyed
`rule/subject`, and carries its `owner`, `due` and `overdue` from the
estate's deadlines, `days_open`, and `as_of`, the day its oldest fact was
read. It links to the
entity its subject came from, through a relation named after that entity's
blueprint, which the sink adds for every list a mapping reads that the hub
has; it links its pillar when `pillar` is set. The hub refuses a relation
to a list it does not have, so a list a mapping names that the hub lacks
gets none until the pass that first finds it, with no restart. An offer
the hub refuses is made again next pass, and the pass is still written, to
the blueprints the hub last took; nothing is written only while the hub
has taken none.

A verdict is one subject judged by one tiered pack, keyed
`pack/kind/subject`: the tier it reached and its rank (1 for the lowest, 0
for none), and the pack's open findings and no-data rules on it. A rule
with no data holds the subject below its tier, so a scorecard that levels
on `tier_rank` never counts missing data as a pass. Its `checks` is an
object of every counted rule of the pack that has a tier and applies to
the subject, by rule id, each `pass` (passed, or failed under a valid
waiver), `fail` (an open finding), or `unknown` (no data), such as
`{"vm.backup": "pass", "vm.drill": "unknown"}`; a rule that does not apply,
a draft, and an announced rule are left out. The subject reaches a tier
exactly when every rule there of that tier or a lower one is `pass`. Draft packs write
none, and `verdicts: false` turns them off. Each pack the board judges with
is written under scope `trellis-packs`, with its version and tiers, its
`level` by the estate's rollup, its `conformant` share, and its `overdue`
count, so the catalog records which version of each standard the estate
adopted and how the estate stands against it. The catalog counts these
fields; it never judges the facts again.

With `rule` set, each rule the pass judged with on the board, written in a
pack or brought by a source, is written with the packs, keyed by its id:
its pack, kind, severity, tier, controls, `status` (`counted`,
`announced`, or `draft`), fix, `source`, and how it answered across the
estate (`pass`, `fail`, `nodata`, `na`, `waived`, `overdue`). With
`waiver` set, each waiver the pass weighed, from every store, is written
with the findings, keyed `store/rule/subject`: its `status` (`active`,
`ending`, `ended`, or `rejected`), the `problem` when it does not count,
its reason, `category`, owner, control, `ends`, the store it came from
(`source`), its `id` where it was granted, and the findings it `covers`.
It links to the entity its subject is when a finding names it. So a
person reads every rule, every verdict, and every exception with its
reason in one place.

With `scorecards` set, the sink also puts one hub scorecard per tiered
pack that is not a draft and judged an entity the catalog holds, whose
rules are the pack's own:

```yaml
sinks:
  orrery:
    scorecards:
      field: trellis_{pack}_checks       # the summary field on each blueprint that holds a pack's checks
      id: trellis_{pack}                 # the scorecard's id; trellis_{pack} when unset
      packs:                             # optional: only these packs, each with its own id or title
        recoverable: { id: recoverable }
        secure: { title: Secure (trellis) }
```

`{pack}` is the pack's id with every character outside `[a-z0-9_]` turned
to `_`. The scorecard's levels are the pack's tiers; on each blueprint the
pack's entities carry, its scope is the entities whose `field` is set, and
each counted tiered rule of the kinds whose subjects carry that blueprint
is a rule of its tier's level, titled by the rule's `aim` (else its title),
with its title as the description and its `fix`, and `judged` by the
entry under its rule id in `field`. Trellis names no blueprint or field:
the field, the ids, and the titles are this setting's, and the blueprints are the
entities' own. A scorecard is put only when it differs from the one last
put, so a steady estate sends nothing, and a failed put never fails the
pass. The hub refuses until an admin gives trellis's source the
`scorecards` grant; trellis says so once per pack. With `packs` set, only
the packs it names get one, so a pack whose kinds the estate gives no
checks field is left out instead of refused each pass. Unset puts none.

The source token needs every blueprint the sink writes into;
the read token needs every list the mappings read.
`deploy/orrery-catalog/model/trellis.yaml` adds, for an estate on the
blueprint standard that writes the names above, a count of open findings,
the tier trellis gave each kind of entity, and each pack's checks as a
summary field, `trellis_<pack>_checks`, which the sink's scorecards read
when `scorecards` sets `field: trellis_{pack}_checks`. It holds no
scorecard: the sink puts them. Every built-in pack has tiers, so every
judged entity has a verdict, and an entity trellis had no data on reaches
no level.

## History and trends

With `history` set, trellis keeps what it judged over time in a
PostgreSQL database it owns, and every pass sends the recent trend along
with the current state:

```yaml
history:
  url: postgres://trellis@db.example.org/trellis?sslmode=require
  sign_in: azure          # Entra token as the pod's workload identity; password uses the URL's
  retention_days: 730     # closed spans and daily totals older than this are dropped
```

It records changes, never copies, so it stays small: a finding is one span
from when it opened to when it closed, a subject's tier in a tiered pack is
one span per tier it held, and each counted pack has one row of totals per
day. A finding whose rule now has no data stays open: no data is not a
fix. Each pack's version is recorded as it changes, so a finding that
opens in the pass its pack changed is marked `cause: rule`, and the
others `cause: subject`. Only counted findings keep history. The tables
are made, and brought up to date, on start.

What each pass sends to the catalog, from the last 90 days:

- A finding: `first_seen` from history, which also sets its deadline, so a
  restart never resets one; `reopened`; and `cause`.
- A verdict: `trend`, a hub `trend` field of the tier ranks it changed to,
  drawn as steps and labelled with the pack's tiers, linking to the subject
  on the board; `tier_since`, `tier_best`, and `tier_direction`.
- A pack: `trend` (the share conformant, one point per day), `change_7d`,
  and `change_30d`.

The trend field holds only the points where a value changed, so a row is
rewritten only when its tier changes, not on every pass. The board's
subject drawer shows the full 90 days, and `GET /v1/history` answers
`?kind=&subject=`, `?rule=`, or `?pack=` with `&days=` up to 1095.

An estate without a catalog trellis writes to sends each pass on with the
`webhook` sink (`sinks.webhook: { url, token }`): the estate's level and
tally, each pack's, pillar's, and owner's, every open finding, and the
pass's events, each with its owner, as JSON. A chat or a ticketing system
routes each event to whoever answers for it.

## Its own telemetry

JSON logs on stdout with `trace_id`; OTLP spans per pass and per source;
metrics `trellis.findings` by rule, pack, severity, and kind,
`trellis.findings.overdue` by rule, pack, and severity, `trellis.waived`,
`trellis.rule.results` by rule, pack, and status (pass, fail, nodata, na,
waived), `trellis.conformant` for the estate and each pack,
`trellis.source.facts` by source and state (missed, carried; a place a
source could not read counts as missed), `trellis.pack.verdict` and `trellis.pack.subjects`,
`trellis.verdict` and `trellis.subjects` per kind, `trellis.pillar.verdict`
per pillar, `trellis.source.run` by source and outcome,
`trellis.source.duration`, `trellis.refresh.age`, `trellis.push`,
`trellis.browser.batches`,
`trellis.build_info`, `http.server.request.duration`, and the process's
`process.cpu.time` and `process.memory.usage`. A rule that does not count
yet, drafted or announced, is never in a finding count. At start, trellis
logs every rule that reads a fact no configured source writes. The page reports browser telemetry as `trellis-web` through
`POST /api/telemetry`.

## Its place in the catalog

trellis never writes itself into a catalog. Its `orrery.yaml` says what
this repository builds: the service `github/orchard-suite/trellis/cmd/trellis`,
the APIs it calls, its logo, and the `fields` and `relations` that place
it. The forge reader of an estate that reads this repository writes that
service and its APIs, so an estate that runs trellis without reading this
repository does not list it.

## Deploying

`deploy/kubernetes/` is a base any estate starts from: a namespace, a
read-only cluster role, the deployment, the two services, an example
`config.yaml` with the estate's positions, a cloud source, a webhook sink,
and a waiver file, and an example pack of the estate's own with its
fixtures and a `tuning.yaml` that narrows its scope. Copy it, change the
config and the packs, create the Secret `trellis-secrets` with the values
the config names, and put a sign-in proxy in front of the Service
`trellis`. `deploy/orrery-catalog/model/trellis.yaml` is the catalog import above.
The image is `ghcr.io/orchard-suite/trellis:<version>`.
