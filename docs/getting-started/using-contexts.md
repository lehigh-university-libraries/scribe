# Use processing contexts

A context is a reusable processing recipe. It chooses one segmentation model,
one transcription provider and model, and any supported transcription options.
Choose a context when the document type, language, layout, or desired
cost/quality balance differs from the workspace default.

Contexts do not contain provider URLs, audiences, or credentials. Scribe shows
only models registered by an administrator, and resolves trusted endpoints and
workspace credentials on the server.

## Choose a context

The **Context** selector appears above the URL, upload, multi-file, and manifest
ingest forms.

- Leave it at **Default** for the workspace default, or the system default when
  the workspace has not set one.
- Choose a named context when the material needs a particular layout detector
  or transcription model.
- For a manifest, imported OCR is preserved. The context drives Canvases that
  have no imported OCR and any explicit reprocessing request.

The selected context applies to the new processing request. Changing the
workspace default later does not silently reprocess existing items or alter the
context snapshot already attached to a queued attempt.

## Create a workspace context

With workspace write access, open **Contexts**, then:

1. Give the context a name that describes the material or purpose, such as
   `German Fraktur — Kraken` rather than a vendor-only name.
2. Choose a transcription provider and one of its registered models.
3. Choose a segmentation model.
4. Add a description so other workspace members know when to select it.
5. Add a system prompt or temperature only when those controls are enabled for
   the selected provider.
6. Select **Set as default** only when this recipe is the best starting point
   for most new processing in the workspace.
7. Select **Create context**.

Workspace contexts are visible only in their workspace. System contexts are
read-only presets visible in every workspace. Creating a new workspace default
replaces the previous workspace default; it does not change the global system
default.

## Pick models deliberately

Segmentation finds regions or words; transcription turns the selected image
regions into text. A strong transcription model cannot recover text that the
segmentation step did not select, so compare both parts of a context when
results are poor.

Useful starting points:

- **Automatic** segmentation runs the built-in detectors and keeps the result
  with more words. Use it as an experiment rather than assuming the larger
  region count is the better layout.
- **Tesseract** segmentation uses the local deterministic detector. Choose the
  Tesseract transcription provider separately when both stages should use it.
- **Scribe** segmentation pairs the built-in detector with the chosen
  transcription provider. The built-in **Scribe Custom** preset is the system
  default when a workspace has not selected its own default.
- **Kraken** choices use administrator-built, digest-pinned model services.

The context library displays run counts. Integrations can use
`ContextService.GetContextMetrics` for corrected-run and average-distance
metrics. Treat a model change as a new experiment: create a clearly named
context, process representative pages, and compare corrections before making
it the workspace default.

## Provider credentials

Providers marked as requiring an API key need a credential under **Settings →
Provider secrets**.

- A **workspace** key can be used by uploads, reprocessing, and background jobs
  and requires workspace administration.
- A **personal** key is limited to foreground editor enrichment and is not
  inherited by queued work.

A queued job that selects a provider without its required workspace key fails
immediately with an actionable job error; it does not fall back to a personal
or deployment-wide credential.

Scribe never copies a provider key into the context. Deleting or rotating a key
therefore does not require recreating contexts.

## Metadata selection rules

API integrations can ask `ContextService.ResolveContext` to select a context
from a bounded flat metadata object. Rules run by descending priority; every
condition in a rule must match, and the first matching rule wins. With no
match, Scribe uses the workspace default and then the system default.

Selection-rule administration is currently an API operation rather than a
control in the context drawer. See
[context catalogs and selection rules](../api/contexts.md) for limits and
pagination behavior.
