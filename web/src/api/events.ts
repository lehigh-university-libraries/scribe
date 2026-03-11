export interface CloudEvent<T = Record<string, unknown>> {
  specversion: string;
  id: string;
  source: string;
  type: string;
  subject?: string;
  time: string;
  datacontenttype?: string;
  data?: T;
}

export interface EventSubscription {
  close: () => void;
}

export function subscribeToEvents(
  options: {
    itemImageId?: string;
    types?: string[];
  },
  onEvent: (event: CloudEvent) => void,
  onError?: (event: Event) => void,
): EventSubscription {
  const params = new URLSearchParams();
  if (options.itemImageId) {
    params.set("item_image_id", options.itemImageId);
  }
  for (const type of options.types ?? []) {
    params.append("type", type);
  }
  const url = `/v1/events${params.toString() ? `?${params.toString()}` : ""}`;
  const source = new EventSource(url);

  source.onmessage = (message) => {
    try {
      onEvent(JSON.parse(message.data) as CloudEvent);
    } catch {
      // Ignore malformed events.
    }
  };
  for (const type of options.types ?? []) {
    source.addEventListener(type, (message) => {
      try {
        const evt = message as MessageEvent<string>;
        onEvent(JSON.parse(evt.data) as CloudEvent);
      } catch {
        // Ignore malformed events.
      }
    });
  }
  source.onerror = (event) => {
    onError?.(event);
  };

  return {
    close: () => source.close(),
  };
}
