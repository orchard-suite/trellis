# ADR-5001: A send is the tail of a pass, never an action of its own

| | |
|---|---|
| **Decision Date** | 2026-11-22 |
| **Decision Maker** | Platform engineering |
| **Last Updated** | 2026-11-22 |
| **Decision Id** | 4a6af90a-277b-4cf6-9d1a-f61069a5837b |

## Status

Accepted

## Audience

Anyone who adds a sink, or a way to make the board act.

## Purpose

Settles whether "run a pass now" and "send now" are one action or two.

## Context

The sinks (the catalog and a webhook) write what a pass judged. After a
catalog fix, the need is to see the catalog take trellis's verdict now.
A send alone would only repeat the last pass, which may be up to the
refresh interval old.

## Decision

- There is one action: run a pass. It reads every source, judges, records
  history, and then calls every sink with what it judged.
- A sink's `Push` returns what it could not deliver. The board keeps each
  sink's outcome on the pass (`Snapshot.Sent`), and the pass never fails
  for it.
- `trellis refresh` asks for a pass on the control port, waits for it and
  its sends, prints each source's and each sink's outcome, and exits 1 when
  one failed.

## What this means for the code

- A new sink is a `board.Sink` with a name, and its `Push` returns an
  error naming what did not land: a failed write, rejected entities, a
  refused scorecard.
- Never add a route or a command that sends without a pass.

## Verification

- The board test `TestAPassSaysHowEachSinkTookIt`.
- The command test `TestRefreshFailsWhenASinkDid`.

## Consequences

- Better: one way to make the board act, and its answer says whether the
  catalog took it.
- Worse: proving a send takes a whole pass, a few minutes on a large
  estate.

## Related

- [ADR-2001](2001-control-port-on-loopback-only.md)
- [ADR-5002](5002-asks-during-a-pass-share-the-next.md)
