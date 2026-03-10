import { createConnectTransport } from "@connectrpc/connect-web";
import type { Transport } from "@connectrpc/connect";

let _transport: Transport | null = null;

export function getTransport(): Transport {
  if (!_transport) {
    _transport = createConnectTransport({ baseUrl: window.location.origin });
  }
  return _transport;
}
