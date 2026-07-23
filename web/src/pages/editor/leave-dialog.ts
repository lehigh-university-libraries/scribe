export interface LeaveDialogElements {
  cancel: HTMLButtonElement;
  dialog: HTMLElement;
  discard: HTMLButtonElement;
  save: HTMLButtonElement;
}

export interface LeaveDialogController {
  close(): void;
  destroy(): void;
  open(): void;
}

export interface LeaveDialogOptions extends LeaveDialogElements {
  onDiscard(): void;
  onSave(): Promise<void>;
}

export function createLeaveDialogController({
  cancel,
  dialog,
  discard,
  onDiscard,
  onSave,
  save,
}: LeaveDialogOptions): LeaveDialogController {
  let returnFocus: HTMLElement | null = null;

  function open() {
    returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.classList.remove("hidden");
    dialog.classList.add("flex");
    cancel.focus();
  }

  function close() {
    dialog.classList.add("hidden");
    dialog.classList.remove("flex");
    returnFocus?.focus();
    returnFocus = null;
  }

  function handleKeyDown(event: KeyboardEvent) {
    if (dialog.classList.contains("hidden")) return;
    if (event.key === "Escape") {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== "Tab") return;

    const buttons = [cancel, discard, save].filter((button) => !button.disabled);
    if (buttons.length === 0) return;
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const next = event.shiftKey
      ? (current <= 0 ? buttons.length - 1 : current - 1)
      : (current >= buttons.length - 1 ? 0 : current + 1);
    event.preventDefault();
    buttons[next]?.focus();
  }

  function handleDiscard() {
    onDiscard();
  }

  async function handleSave() {
    save.disabled = true;
    try {
      await onSave();
    } finally {
      save.disabled = false;
    }
  }

  cancel.addEventListener("click", close);
  discard.addEventListener("click", handleDiscard);
  save.addEventListener("click", handleSave);
  dialog.addEventListener("keydown", handleKeyDown);

  return {
    close,
    destroy() {
      cancel.removeEventListener("click", close);
      discard.removeEventListener("click", handleDiscard);
      save.removeEventListener("click", handleSave);
      dialog.removeEventListener("keydown", handleKeyDown);
    },
    open,
  };
}
