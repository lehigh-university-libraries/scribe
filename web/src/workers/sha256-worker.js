self.addEventListener('message', async (event) => {
  try {
    const file = event.data?.file;
    if (!(file instanceof Blob)) throw new Error('A file is required');
    const bytes = await file.arrayBuffer();
    const digest = await crypto.subtle.digest('SHA-256', bytes);
    const hex = Array.from(new Uint8Array(digest), (part) => part.toString(16).padStart(2, '0')).join('');
    self.postMessage({ digest: hex });
  } catch {
    self.postMessage({ error: 'File hashing failed' });
  }
}, { once: true });
