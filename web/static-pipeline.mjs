import { pipeline } from "node:stream/promises";
import { isExpectedDisconnect } from "./proxy-pipeline.mjs";

/**
 * Streams one static asset while coupling the file descriptor to the browser
 * connection. Expected client disconnects are contained; disk/read failures
 * reject so the request boundary can return or terminate the response safely.
 */
export async function pipeStaticResponse(source, response) {
  const closeSource = () => {
    if (!source.destroyed) source.destroy();
  };
  response.once("close", closeSource);
  try {
    await pipeline(source, response);
  } catch (error) {
    if (!isExpectedDisconnect(error)) throw error;
  } finally {
    response.removeListener("close", closeSource);
  }
}
