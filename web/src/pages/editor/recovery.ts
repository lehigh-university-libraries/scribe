import { workspaceAwarePath } from "../../lib/workspace";
import { html, setHTML } from "../../lib/util";

export interface EditorRecoveryOptions {
  message: string;
  retry?: () => void;
  retryLabel?: string;
}

export function renderEditorRecovery(
  meta: HTMLElement,
  viewer: HTMLElement | null,
  {
    message,
    retry,
    retryLabel = "Retry",
  }: EditorRecoveryOptions,
): void {
  meta.textContent = message;
  meta.setAttribute("aria-live", "assertive");
  meta.setAttribute("role", "alert");
  if (!viewer) return;

  setHTML(
    viewer,
    html`<div class="flex h-full items-center justify-center p-6">
      <section
        aria-labelledby="editor-recovery-title"
        class="w-full max-w-lg rounded-lg border border-destructive/30 bg-card p-6 text-card-foreground shadow-lg"
        role="alert"
      >
        <h2 id="editor-recovery-title" class="text-lg font-semibold">
          Editor could not open this page
        </h2>
        <p class="mt-2 text-sm text-muted-foreground">${message}</p>
        <div class="mt-5 flex flex-wrap gap-2">
          ${retry
            ? html`<button
                id="editor-recovery-retry"
                class="inline-flex items-center rounded-md bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
                type="button"
              >
                ${retryLabel}
              </button>`
            : ""}
          <a
            class="inline-flex items-center rounded-md border border-border bg-background px-3.5 py-2 text-sm font-medium text-foreground hover:bg-accent hover:text-accent-foreground"
            href="${workspaceAwarePath("/")}"
          >
            Back to library
          </a>
        </div>
      </section>
    </div>`,
  );

  if (retry) {
    document
      .getElementById("editor-recovery-retry")
      ?.addEventListener("click", retry, { once: true });
  }
}
