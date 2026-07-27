const STYLE_ID = 'scribe-plugin-theme';

/**
 * Mirador can be embedded outside the bundled shell, so the plugin owns a
 * small semantic token layer. The values inherit the shell's standard theme
 * tokens when present and provide light/dark fallbacks for standalone use.
 */
export function ensureScribePluginTheme() {
  if (typeof document === 'undefined' || document.getElementById(STYLE_ID)) return;
  const style = document.createElement('style');
  style.id = STYLE_ID;
  style.textContent = `
    :root {
      --scribe-plugin-background: var(--background, #ffffff);
      --scribe-plugin-surface: var(--card, #ffffff);
      --scribe-plugin-surface-muted: var(--muted, #f4f4f5);
      --scribe-plugin-foreground: var(--foreground, #09090b);
      --scribe-plugin-muted-foreground: var(--muted-foreground, #71717a);
      --scribe-plugin-border: var(--border, #e4e4e7);
      --scribe-plugin-accent: var(--accent, #f4f4f5);
      --scribe-plugin-accent-foreground: var(--accent-foreground, #18181b);
      --scribe-plugin-selected: #fef3c7;
      --scribe-plugin-selected-foreground: #92400e;
      --scribe-plugin-line: #2563eb;
      --scribe-plugin-line-surface: #dbeafe;
      --scribe-plugin-word: #d97706;
      --scribe-plugin-word-surface: #fef3c7;
      --scribe-plugin-transcribe: #6d28d9;
      --scribe-plugin-transcribe-strong: #5b21b6;
      --scribe-plugin-overlay: rgb(15 23 42 / 0.38);
      --scribe-plugin-overlay-surface: rgb(15 23 42 / 0.82);
      --scribe-plugin-overlay-foreground: #f8fafc;
      --scribe-plugin-shadow: rgb(15 23 42 / 0.14);
      --scribe-plugin-shadow-soft: rgb(15 23 42 / 0.08);
    }

    :root[data-theme="dark"] {
      --scribe-plugin-selected: #451a03;
      --scribe-plugin-selected-foreground: #fde68a;
      --scribe-plugin-line: #60a5fa;
      --scribe-plugin-line-surface: #172554;
      --scribe-plugin-word: #fbbf24;
      --scribe-plugin-word-surface: #451a03;
      --scribe-plugin-overlay: rgb(0 0 0 / 0.52);
      --scribe-plugin-overlay-surface: rgb(9 9 11 / 0.9);
      --scribe-plugin-overlay-foreground: #fafafa;
      --scribe-plugin-shadow: rgb(0 0 0 / 0.42);
      --scribe-plugin-shadow-soft: rgb(0 0 0 / 0.28);
    }
  `;
  document.head.appendChild(style);
}

export const scribeTheme = Object.freeze({
  accent: 'var(--scribe-plugin-accent)',
  accentForeground: 'var(--scribe-plugin-accent-foreground)',
  background: 'var(--scribe-plugin-background)',
  border: 'var(--scribe-plugin-border)',
  foreground: 'var(--scribe-plugin-foreground)',
  line: 'var(--scribe-plugin-line)',
  lineSurface: 'var(--scribe-plugin-line-surface)',
  mutedForeground: 'var(--scribe-plugin-muted-foreground)',
  overlay: 'var(--scribe-plugin-overlay)',
  overlayForeground: 'var(--scribe-plugin-overlay-foreground)',
  overlaySurface: 'var(--scribe-plugin-overlay-surface)',
  selected: 'var(--scribe-plugin-selected)',
  selectedForeground: 'var(--scribe-plugin-selected-foreground)',
  shadow: 'var(--scribe-plugin-shadow)',
  shadowSoft: 'var(--scribe-plugin-shadow-soft)',
  surface: 'var(--scribe-plugin-surface)',
  surfaceMuted: 'var(--scribe-plugin-surface-muted)',
  transcribe: 'var(--scribe-plugin-transcribe)',
  transcribeStrong: 'var(--scribe-plugin-transcribe-strong)',
  word: 'var(--scribe-plugin-word)',
  wordSurface: 'var(--scribe-plugin-word-surface)',
});

ensureScribePluginTheme();
