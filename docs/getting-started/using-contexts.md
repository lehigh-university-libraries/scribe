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

While an image URL is processing, **Process URL** is disabled to prevent duplicate
submissions. A successful request opens the editor; if processing fails, the form
shows the error and enables the button so you can retry.

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

For Gemini, choose **Google Gemini** and select `gemini-3.1-pro-preview`
(Pro) or `gemini-3.8-flash` (Flash). These are the latest Pro and Flash options
in [Google's model catalog](https://ai.google.dev/gemini-api/docs/models)
as of October 8, 2026. Select **Kraken** segmentation to pair its layout
detection with Gemini transcription, and **Set as default** to use this
context for new workspace processing.

## Pick models deliberately

Segmentation finds regions or words; transcription turns the selected image
regions into text. A strong transcription model cannot recover text that the
segmentation step did not select, so compare both parts of a context when
results are poor.

The built-in presets cover these starting collections:

| Material | Segmentation |
| --- | --- |
| Latin-script handwritten letters | Kraken BLLA line detection |
| Latin-script medieval manuscripts | Kraken BLLA line detection |
| Historical English newspapers | PP-DocLayoutV3 layout reading order, with Kraken BLLA lines |

Each material has **GLM-OCR**, **Gemini Pro**, **Gemini Flash**, and **OpenAI**
presets. **Letters + GLM-OCR** is the system default. Every detected line is
transcribed from its own crop; columns and narrow marginal notes retain their
original bounds. Newspaper layout orders the detected lines by column; lines
outside the detected layout regions remain available at the end in Kraken order.

Transcription choices are glm-ocr:bf16, gemini-3.1-pro-preview,
gemini-3.8-flash, and registered OpenAI vision models. Segmentation does not
perform recognition. Tesseract, custom automatic detectors, and CATMuS
recognition are no longer available.

These are starting selections, not a measured ranking for your collections.
Compare representative pages before declaring a model best for a collection.
The architecture also supports additional languages, scripts, and layouts:
create workspace contexts using server-registered models and selection rules.
No collection assumption changes the IIIF storage or provider transport.

The context library displays run counts. Integrations can use
`ContextService.GetContextMetrics` for corrected-run and average-distance
metrics. Treat a model change as a new experiment: create a clearly named
context, process representative pages, and compare corrections before making
it the workspace default.

## Provider credentials

Providers marked as requiring an API key need a credential under **Settings →
Provider secrets**.

To enable Gemini for a new upload:

1. Open the workspace/account **Settings** panel.
2. Under **Provider secrets**, choose **Google Gemini** and enter a name for the
   credential.
3. Choose **Workspace (queued processing)**. This option requires workspace
   administrator access.
4. Paste the key into **Provider API key**, then select **Save provider key**.
5. Return to the Library, choose the intended Gemini context above the upload
   form, and then upload the document.

- A **workspace** key powers durable automatic transcription for image-URL
  ingest, uploaded files, reprocessing, and other queued jobs, and requires
  workspace administration.
- A **personal** key is limited to foreground editor enrichment and is not
  inherited by queued work.

This boundary follows the operation, not the browser location. When an upload
or image-URL ingest opens the editor while automatic transcription continues,
the work remains a durable workspace job and uses the workspace key. A personal
key is considered only for an explicit foreground editor enrichment such as a
manual line or page retranscription.

Foreground editor enrichment prefers the newest active personal key for the
selected provider over a workspace key. If the provider rejects that
credential, replace or delete the personal key; Scribe does not silently retry
the request with a workspace credential.

A queued job that selects a provider without its required workspace key fails
immediately with an actionable job error; it does not fall back to a personal
or deployment-wide credential.

Do not put a Gemini key in `.env` or `GEMINI_API_KEY`; Scribe does not consume
that variable for provider credentials. The deployment-wide secret written by
`make vault-secrets` is also not eligible for durable workspace jobs. Store the
key through **Provider secrets** so queued work resolves the workspace-scoped
secret-store locator.

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
