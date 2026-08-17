# Add a system context

System contexts are read-only presets visible in every workspace. They are
declared in `internal/app/contexts.go`, validated against the installed model
registry before startup, and convergently seeded into MariaDB by
`EnsureSystemContexts`.

Use a system context only for a broadly useful, operator-supported recipe.
Material-specific experiments belong in a workspace context.

## Add a preset

1. Ensure every segmentation and transcription selection is already registered
   and deployable. Follow the model guides before referring to a new model.
2. Add one `store.Context` to `systemContexts`. Give it a stable, unique name
   and a description that tells users when to choose it.
3. Use a provider's configured default through
   `Registry.EffectiveModel` when the preset should track that provider.
   Hard-code a model only when the preset deliberately names that immutable
   selection.
4. Set a system prompt or temperature only when the descriptor advertises the
   matching capability. Tesseract and Kraken reject unsupported prompt
   controls.
5. Leave `UserID` and `WorkspaceID` unset. Startup owns system scope; client
   input must never be able to create it.
6. Add a focused test in `internal/app/contexts_test.go` for the name, model,
   prompt/capability behavior, and startup validation.
7. Start against an isolated database twice and prove the same named row is
   updated rather than duplicated.

Names are the convergence key. Renaming a shipped preset creates a new row and
does not remove the old name, so treat a rename or retirement as an explicit
catalog lifecycle change with an acceptance test.

## Change the global default

There is exactly one system default. Change the recipe produced by
`defaultContext`; do not add a second system context with `IsDefault: true`.
The built-in **Tesseract OCR** default uses the established Scribe segmentor
with credential-free local Tesseract line transcription.
Provider-specific presets such as **Gemini Pro** remain explicit selections,
while **Kraken BLLA** continues to use the administrator-configured
`llm.provider`, that provider's default model, and its supported
`llm.default_system_prompt`.

When replacing or removing a shipped default, list the replacement as the sole
catalog default and use `ContextStore.ReplaceSystemDefault` at startup to
promote it and explicitly retire the old stable name in one transaction.
Removing a value from `systemContexts` alone does not remove its persisted row.

Workspace defaults still take precedence. Changing the system default affects
workspaces that have not selected their own default and future processing
requests; it does not rewrite persisted job snapshots or canonical annotation
revisions.

Test the new default with:

- configuration and provider-registry validation;
- `validateSystemContextCatalog`;
- concurrent startup seeding against an isolated database;
- a processing request with no explicit context ID;
- a workspace that has its own default, proving the system change does not
  override it.
