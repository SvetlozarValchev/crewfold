# Context packets and live deltas

Crewfold context is an immutable, bounded authority snapshot for one task/agent
run. It is not a transcript, a search result, a shared append-only prompt, or a
provider session export.

## Current packet

`context build` creates an unbound current packet; `run.start` may bind that explicit
packet once, or build and bind one atomically. The packet contains the exact role,
task, checkout, direct dependencies, bounded reverse dependents, inbox previews,
authorized participant-thread rosters, explicitly selected canonical knowledge,
reporting instructions, and a frozen live-delivery policy. It also records the
global event cursor through which the snapshot was assembled.

Direct dependencies are complete and packet construction fails above 32. The
informational reverse-dependent list carries at most 32 whole snapshots plus
`dependent_task_count`, so omitted breadth remains visible without granting new
authority.

The packet stays byte-for-byte immutable. Search results are never added
implicitly. Participant rosters are informational and do not grant thread access;
the authenticated run must still match an exact agent/task/project participant
binding. Reverse dependents are informational and remain project-scoped.

An explicitly prebuilt packet is revalidated once when `run.start` binds it. If
its frozen run authority no longer has canonical provenance, or any embedded
knowledge is no longer accepted, current, fresh, applicable, and undisputed, the
binding fails and the owner must build a new packet. Building and binding in the
same run-start transaction has no intervening window. After a successful binding,
the base bytes remain immutable: reads do not re-evaluate them, and later changes
appear only through an explicit withdrawal/delta or durable rebase.

### Optional delegated grants

The same current packet may add one exact manager-grant snapshot or one exact
project-scoped check-watch grant snapshot. It may contain neither, but cannot
contain both grant families.

The check-watch snapshot freezes grant/project, exact enabled agent revision, exact
allowlisted definition revisions, the closed `run|inspect|propose_repair`
operation subset, quantitative limits, canonical hash, and expiry. Binding and
every check mutation revalidate the live run and current exact grant. Revocation
leaves the immutable packet readable and denies later calls.

Agent role and launch-profile purpose remain display/workflow metadata and never
alter the packet schema, tools, definition set, routing, or repair authority.

## Refresh and delivery

Live delivery separates three facts:

1. The owner explicitly asks Crewfold to inspect changes with `context refresh`.
2. The exact bound run fetches the sole pending delta through
   `crewfold_get_context_delta`.
3. That run attests consumption through
   `crewfold_acknowledge_context_delta`.

Only step 1 constructs a delta. The MCP fetch tool has no arguments and cannot
choose or advance a scope. There is no owner acknowledgement command because an
owner cannot truthfully assert what the agent consumed.

Refresh returns `created`, `pending`, `up_to_date`, or `rebase_required`. At most
one delta is pending. While it remains unacknowledged, every refresh returns that
same immutable object without scanning past it. A no-change refresh advances a
durable inspected cursor but creates no empty delta and no event.

After daemon restart, the pending delta and its acknowledgement state remain
available. After a successful exact-run acknowledgement, the next explicit
refresh can produce the next sequence.

## Delivered changes

A delta is a deterministic, closed union of whole canonical changes:

- a scoped inbox summary preview, never a full message body;
- a newly applicable accepted decision, or an eligible decision re-offered after
  its final applicable contradiction closes;
- withdrawal of knowledge already known to the run, or a durable no-body
  `disputed` suppression tombstone for a post-base accepted applicable decision
  hidden by an open contradiction;
- an opened or closed exact contradiction and its affected revisions;
- an added or changed same-project reverse dependent; or
- a full authorized participant-thread roster after creation or invitation.

Proposed contradictions remain inert. Newly accepted findings do not flow into a
run automatically in delta v1, although an already known finding can be
withdrawn. Once the final applicable contradiction closes, an eligible decision
is re-offered when it was either delivered and then withdrawn solely as disputed,
or never delivered but recorded by the exact suppression tombstone. An open
contradiction snapshot alone does not imply that either participant was delivered
or suppressed and cannot create re-offer eligibility. Findings and otherwise-
ineligible decisions are not re-offered. Cross-project participant mail is
eligible only for the exact bound task; another run of the same agent on another
task cannot receive its preview or roster.

Freshness expiry is evaluated by explicit refresh. It can produce a withdrawal
without a new knowledge event, so a time-driven delta may have equal `from` and
`through` event cursors.

## Bounds and rebase

The packet freezes these live limits:

- one pending delta;
- at most 1,000 potentially applicable events per refresh;
- at most 16 KiB for one encoded delta; and
- at most 64 KiB for the delta chain.

Crewfold coalesces events to current canonical state and either includes each
change whole or does not build a delta. It never truncates a revision, preview,
roster, contradiction, or dependency. A changed base contract or direct-dependency
set, event-window overflow, delta overflow,
cumulative overflow, or unsupported authority-changing event becomes durable
`rebase_required` state.

Rebase is an explicit handoff boundary: stop or hand off the existing run, build a
new current packet, and start a replacement run. Crewfold does not mutate the base
packet or provider transcript in place.

## Inspection versus consumption

The owner can list, show, and explain immutable deltas through the local API or
CLI. These queries are audit and troubleshooting surfaces only. They do not mark
a delta delivered or consumed.

The event journal records delta construction, exact-run acknowledgement, and
rebase. It does not record a no-op cursor advance. Event payloads identify why a
projection must be reloaded; the canonical projections, not event prose, are the
content authority.

See [ADR-0014](decisions/0014-explicit-bounded-live-context-deltas.md) for the
full authority, cursor, and failure decision.
