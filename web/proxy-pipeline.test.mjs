import { PassThrough } from "node:stream";
import { describe, expect, it, vi } from "vitest";
import { pipeUpstreamResponse } from "./proxy-pipeline.mjs";

describe("frontend proxy stream lifecycle", () => {
  it("destroys the upstream when the browser disconnects", async () => {
    const upstream = new PassThrough();
    const downstream = new PassThrough();
    const onError = vi.fn();
    pipeUpstreamResponse(upstream, downstream, onError);

    downstream.destroy(Object.assign(new Error("browser reset"), { code: "ECONNRESET" }));
    await new Promise((resolve) => setImmediate(resolve));

    expect(upstream.destroyed).toBe(true);
    expect(onError).not.toHaveBeenCalled();
  });

  it("contains an upstream reset and reports it through the pipeline callback", async () => {
    const upstream = new PassThrough();
    const downstream = new PassThrough();
    downstream.resume();
    const onError = vi.fn();
    pipeUpstreamResponse(upstream, downstream, onError);
    upstream.destroy(Object.assign(new Error("bad upstream"), { code: "EBADMSG" }));
    await new Promise((resolve) => setImmediate(resolve));

    expect(downstream.destroyed).toBe(true);
    expect(onError).toHaveBeenCalledOnce();
  });
});
