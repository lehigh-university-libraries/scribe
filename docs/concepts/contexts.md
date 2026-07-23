# Contexts

A context is a first-class resource that selects and configures one segmentation
model and one transcription model. Contexts let users reuse a processing setup
across items without embedding provider switches throughout the UI.

A context may include selection metadata used to resolve the best context for
an image. Provider endpoints and credential policy remain administrator-owned;
workspaces select registered capabilities rather than arbitrary authenticated
network destinations.

`ContextService.GetModelCatalog` is the client discovery contract. Its
descriptors intentionally contain only safe selection metadata. The server
builds the catalog from trusted configuration, validates every context against
it on create/update, and resolves endpoint, audience, and credentials only when
executing a job. Context protobuf fields for client-controlled provider URLs or
audiences are reserved and must not be reintroduced.

Context quality is read through the generated Connect
`ContextService.GetContextMetrics` RPC. It summarizes committed processing and
correction data; there is no parallel REST metrics route.

Context and selection-rule discovery uses signed workspace-bound keyset pages;
see [context catalogs and selection rules](../api/contexts.md). Resolution is
deliberately finite: workspace rule admission, rule conditions, and flat scalar
metadata all have explicit ceilings so adding contexts cannot turn a processing
request into an unbounded database scan or string-matching workload.

When adding a context field:

1. define its semantics and default in the protobuf contract;
2. validate it at the API boundary;
3. prove the runtime consumes it;
4. expose it from the provider or segmentor capability descriptor;
5. add an API and UI round-trip test.

Do not add configuration-shaped fields that the processing pipeline ignores.

For practical workflows, see [use processing contexts](../getting-started/using-contexts.md).
For catalog extensions, see [add a transcription model](../development/adding-transcription-model.md),
[add a segmentation model](../development/adding-segmentation-model.md), and
[add a system context](../development/adding-system-context.md).
