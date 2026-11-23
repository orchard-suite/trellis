# Decisions

This folder is trellis's record of decisions: one decision per record,
with the reasons, so the next person or agent builds the same way or
knows which record to supersede.

| Prefix | Category | What it covers |
|---|---|---|
| 0xxx | Governance | How we decide and govern. |
| 1xxx | Infrastructure | What we build on. |
| 2xxx | Security | Identity, auth, secrets, access. |
| 3xxx | Data | Databases, storage, retention. |
| 4xxx | Compute | How workloads run. |
| 5xxx | Development | Architecture inside the code, CI/CD, testing. |
| 6xxx | Operations | Monitoring, alerting, recovery. |

## Records

### Security

| Number | Title | Status | Last updated |
|---|---|---|---|
| [2001](2001-control-port-on-loopback-only.md) | The control port listens on loopback only | Accepted | 2026-11-22 |

### Development

| Number | Title | Status | Last updated |
|---|---|---|---|
| [5001](5001-a-send-is-the-tail-of-a-pass.md) | A send is the tail of a pass, never an action of its own | Accepted | 2026-11-22 |
| [5002](5002-asks-during-a-pass-share-the-next.md) | An ask during a pass gets the next pass, shared by every ask | Accepted | 2026-11-22 |

## Adding or changing a record

- Copy `0000-template.md` to `CNNN-short-title.md`: the category's first
  digit, then the next number in it. A number is never reused.
- Status is Proposed, Accepted, Deprecated, or Superseded by ADR-CNNN.
- Strengthen a record in place while its decision stands, and update Last
  Updated. When the decision is reversed, write a new record and mark the
  old one superseded.
- Update this index in the same commit.
