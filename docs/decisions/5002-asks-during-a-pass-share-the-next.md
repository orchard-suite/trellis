# ADR-5002: An ask during a pass gets the next pass, shared by every ask

| | |
|---|---|
| **Decision Date** | 2026-11-22 |
| **Decision Maker** | Platform engineering |
| **Last Updated** | 2026-11-22 |
| **Decision Id** | 439edd7f-55c0-4a50-94b6-90c8241c4643 |

## Status

Accepted

## Audience

Anyone who changes how the board schedules passes.

## Purpose

Settles what happens when a pass is asked for while one runs.

## Context

The board ran one pass at a time and dropped any refresh asked for during
one. The running pass may have read its sources before the change the
asker wants seen, so handing back its result answers the wrong question,
and dropping the ask leaves the asker waiting for the schedule.

## Decision

- An ask (`Board.Check`) gets a pass that starts after the ask.
- While a pass runs, the ask becomes the pass right after it. Every ask
  made until that pass starts shares it, so many asks cost one pass.
- The asked pass runs to its end even when the asker stops waiting.
- The schedule's own pass (`Board.Refresh`) still skips while one runs.

## What this means for the code

- Passes run one at a time; `drive` hands the running state to the queued
  pass or clears it.
- The session port's "Check now" and the control port's refresh both use
  `Board.Check`.

## Verification

- The board test `TestAnAskDuringAPassGetsThePassAfterIt`.

## Consequences

- Better: an ask is never lost and always sees what happened before it.
- Worse: an ask can wait for up to two passes.

## Related

- [ADR-5001](5001-a-send-is-the-tail-of-a-pass.md)
