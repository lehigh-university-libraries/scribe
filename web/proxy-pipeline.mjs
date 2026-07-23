import { pipeline } from "node:stream";

export function isExpectedDisconnect(error) {
  return error?.code === "ERR_STREAM_PREMATURE_CLOSE"
    || error?.code === "ECONNRESET"
    || error?.code === "EPIPE";
}

/**
 * Couples an upstream response to the browser connection. Closing either side
 * tears down the other, so abandoned downloads do not retain upstream sockets.
 */
export function pipeUpstreamResponse(upstreamResponse, downstreamResponse, onError = () => {}) {
  const closeUpstream = () => {
    if (!upstreamResponse.readableEnded && !upstreamResponse.destroyed) {
      upstreamResponse.destroy();
    }
  };
  downstreamResponse.once("close", closeUpstream);
  pipeline(upstreamResponse, downstreamResponse, (error) => {
    downstreamResponse.removeListener("close", closeUpstream);
    if (error && !isExpectedDisconnect(error)) onError(error);
  });
}

export function pipeUpstreamRequest(incomingRequest, upstreamRequest, onError = () => {}) {
  pipeline(incomingRequest, upstreamRequest, (error) => {
    if (error && !isExpectedDisconnect(error)) onError(error);
  });
}
