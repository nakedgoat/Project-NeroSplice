# Continuity Roadmap

This document tracks the hard migration gaps that are not solved by the current API-bridge tool.

## Original room IDs

Current status:

- Not preserved.
- The tool creates new rooms on the target and stores source-to-target mapping in `migration_state.json`.

Why:

- Room IDs are created by the target homeserver when the room is created.
- Dendrite does not expose a supported import API for injecting an externally defined room with an existing room ID.

Practical next steps:

- Preserve the same server name on the target when possible.
- Export a room ID mapping file as a first-class artifact.
- Recreate canonical aliases and publish a mapping document for clients and operators.
- If exact room IDs become a hard requirement, the likely path is a Dendrite-side custom importer, not a client API bridge.

## Message history

Current status:

- Not imported.

Why:

- The bridge tool only recreates current room structure and selected state.
- Dendrite does not expose a stable bulk history import path through the client APIs used here.

Practical next steps:

- Add a source-side export command that writes room history to NDJSON or JSONL for archival.
- Add exported event-to-room metadata so operators can preserve message evidence even when messages cannot be replayed authentically.
- Explore whether a custom Dendrite importer or offline DB transformer is feasible, but treat that as a separate high-risk project.

## Password hashes

Current status:

- Not preserved.
- Users get deterministic temporary passwords and must reset after cutover.

Why:

- The current tool uses shared-secret registration and client login flows only.
- There is no stable target-side API for inserting Synapse password hashes directly.

Practical next steps:

- Short-term: keep temp password CSV generation and communications workflow.
- Better medium-term option: move auth to an external identity provider before migration so both homeservers delegate authentication to the same backend.
- High-risk option: patch Dendrite or write a one-off importer that can accept Synapse hash material if the hash formats are compatible.

## E2E keys

Current status:

- Not preserved.

Why:

- Server-side APIs do not give this tool access to the private device secrets users need for seamless E2E continuity.
- Cross-signing state, SSSS, and Megolm session continuity are fundamentally client-side concerns.

Practical next steps:

- Build a user-run export/import workflow around client-side secret export before cutover.
- Document supported client paths for secure backup restore after first login on the new homeserver.
- Investigate limited migration of device metadata and key backup versions, but do not assume that preserves usable decryption history.

## Recommended direction

The most realistic continuity plan is:

1. Use this tool for accounts, profiles, room shell/state, and media.
2. Add archival export for room history and room mappings.
3. Move authentication to a shared IdP if password continuity matters.
4. Treat E2E continuity as a client-managed backup and restore process.

Anything beyond that likely requires custom Dendrite server work rather than more client API glue.
