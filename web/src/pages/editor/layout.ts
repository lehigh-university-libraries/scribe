import { html, setHTML } from "../../lib/util";

export function renderEditorLayout(app: HTMLElement): void {
  setHTML(app, html`
    <main class="h-screen w-screen overflow-hidden bg-background text-foreground">
      <header class="flex items-center justify-between border-b border-border bg-background/95 px-4 py-2">
        <div class="flex items-center gap-4">
          <a href="/" class="text-lg font-bold tracking-tight">Scribe</a>
          <nav class="flex items-center gap-2 text-sm text-muted-foreground">
            <button id="home-nav" class="inline-flex items-center gap-2 rounded-md border bg-background px-3 py-2 text-sm font-medium text-foreground shadow-xs transition hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50">Home</button>
            <button id="reprocess-nav" class="inline-flex items-center gap-2 rounded-md border bg-background px-3 py-2 text-sm font-medium text-foreground shadow-xs transition hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50">Resegment + retranscribe</button>
          </nav>
        </div>
        <div class="text-right">
          <h1 class="text-xl font-bold">Editor</h1>
          <p id="editor-meta" class="text-xs text-muted-foreground"></p>
          <p id="editor-transcription-status" class="mt-1 text-xs text-destructive"></p>
        </div>
      </header>
      <section class="relative h-[calc(100vh-56px)]">
        <div id="editor-batch-banner" class="hidden pointer-events-none absolute inset-x-0 top-0 z-40 px-4 py-4">
          <div class="mx-auto flex max-w-6xl items-start justify-between gap-4 rounded-lg border border-destructive/30 bg-background/95 px-4 py-3 shadow-2xl backdrop-blur">
            <div>
              <p id="editor-batch-banner-title" class="text-sm font-semibold text-destructive"></p>
              <p id="editor-batch-banner-detail" class="mt-1 text-sm text-destructive/80"></p>
            </div>
          </div>
        </div>
        <div id="mirador-viewer" class="h-full w-full"></div>
      </section>
      <div id="leave-dialog" class="hidden fixed inset-0 z-50 items-center justify-center bg-foreground/20">
        <div class="w-full max-w-md rounded-lg border border-border bg-card p-6 text-card-foreground shadow-2xl">
          <h2 class="text-lg font-semibold">Leave editor?</h2>
          <p class="mt-2 text-sm text-muted-foreground">You have unsaved changes. Save before returning home?</p>
          <div class="mt-5 flex justify-end gap-2">
            <button id="leave-cancel" class="inline-flex items-center gap-2 rounded-md border bg-background px-3.5 py-2 text-sm font-medium text-foreground shadow-xs transition hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50">Cancel</button>
            <button id="leave-discard" class="inline-flex items-center gap-2 rounded-md border bg-background px-3.5 py-2 text-sm font-medium text-destructive shadow-xs transition hover:bg-destructive/10 disabled:pointer-events-none disabled:opacity-50">Discard</button>
            <button id="leave-save" class="inline-flex items-center gap-2 rounded-md bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50">Save</button>
          </div>
        </div>
      </div>
    </main>
  `);
}
