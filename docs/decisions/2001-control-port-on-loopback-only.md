# ADR-2001: The control port listens on loopback only

| | |
|---|---|
| **Decision Date** | 2026-11-22 |
| **Decision Maker** | Platform engineering |
| **Last Updated** | 2026-11-22 |
| **Decision Id** | 6fb8a1dd-374e-4ed1-b8d1-f635e1df0b35 |

## Status

Accepted

## Audience

Anyone who adds a route to trellis's server or changes where it listens.

## Purpose

Settles who may ask a running board to act, such as running a pass, and how
trellis knows they may.

## Context

An operator needs to make the board run a pass now, for example after a
catalog blueprint gained the fields the sink writes. The session port
trusts the sign-in proxy's headers and is reached through a browser. The
health port trusts nothing and must stay that way for the kubelet. A
machine caller, such as an agent or a person at a terminal, had no way in
short of restarting the board.

## Decision

- The board serves a third port, the control port, `listen.control`. It is
  `127.0.0.1:8082` unless set.
- trellis refuses to start when `listen.control` is not a loopback address
  (`127.0.0.1`, `::1`, or `localhost`).
- The control port carries no identity of its own. Whoever can reach
  loopback inside the pod may ask: in a cluster, whoever Kubernetes lets
  exec into the pod or forward a port to it. That grant is the gate.
- The control port holds operator actions only. Read routes stay on the
  session port behind the sign-in.

## What this means for the code

- `internal/config` checks the address; never relax the check.
- A new operator action is a route on `Server.Control()`, never on the
  health port, and never a session route that skips the gate.

## Verification

- `internal/config` tests refuse `:8082`, `0.0.0.0:8082`, and a private
  address, and accept the loopback forms.
- The server tests prove the health port does not run a pass.

## Consequences

- Better: an operator or agent with cluster access needs no token, no
  browser, and no restart.
- Worse: the gate is cluster access, so anyone who can exec into the pod
  can make the board work. A pass reads every source, so asks cost API
  calls; asks during a pass share one pass ([ADR-5002](5002-asks-during-a-pass-share-the-next.md)).

## Related

- [ADR-5001](5001-a-send-is-the-tail-of-a-pass.md)
