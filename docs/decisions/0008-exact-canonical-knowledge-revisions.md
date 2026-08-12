# ADR-0008: Exact canonical knowledge revisions

- Status: accepted
- Date: 2026-08-13

## Context

Replacement agents need decisions and findings that remain useful without access
to another provider's transcript. A mutable note or automatic “latest” lookup is
not reproducible: governance can change between packet construction and run
start, and silently substituting a successor changes what an explicit revision
link means. Provenance also cannot double as applicability—a finding sourced from
one task often needs to guide a later task in the same project.

The local API socket is owner-only, while run-scoped MCP capabilities authenticate
agents. Allowing a mutation payload to name its own approving actor would weaken
that authority boundary.

## Decision

Crewfold stores a stable knowledge item and immutable content revisions. Review
state (`proposed`, `accepted`, or `rejected`) is separate from currency
(`pending`, `current`, `stale`, or `superseded`). A successor is proposed as a new
revision; accepting it and superseding its predecessor is one transaction. Bodies
and frozen structured sources are never rewritten or deleted.

Task, concluded-meeting, and accepted-meeting-proposal references are provenance.
Applicability is stored independently as project scope with an optional task
scope. The first bounded implementation supports decisions and findings.

Run-scoped agents may propose knowledge. Only the trusted local owner may accept,
reject, or mark it stale. Public mutation parameters never contain an actor or
approver field. Runs have no governance tool: a reserved-name probe is rejected
and audited by the immutable capability layer. Every operation that reaches the
internal knowledge-governance boundary records an authority decision; a non-owner
attempt commits its denial record without changing the proposal.

Context builds accept an ordered, bounded list of exact knowledge revision IDs.
They include only accepted, current, fresh, applicable revisions that fit the
budget, copying complete snapshots into immutable packet v3. Ineligible revisions
are explained individually. A superseded exact pin is excluded and names its
current replacement, but Crewfold never follows it silently. Eligibility is
evaluated when the packet is built; later governance does not mutate or invalidate
that packet.

## Consequences

- A run can reproduce exactly which accepted statements it received.
- Supersession preserves history and requires callers to opt into new wording.
- Provenance remains auditable without making a source task the only consumer.
- Authority cannot be spoofed through the owner API or an agent tool call.
- Packet construction has deterministic whole-item and total byte budgets.
- Callers that require current truth must build a new packet; automatic retrieval,
  contradiction handling, and context deltas remain separate later capabilities.
