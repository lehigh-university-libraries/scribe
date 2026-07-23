import { PassThrough, Readable } from "node:stream";
import { describe, expect, it } from "vitest";

import { pipeStaticResponse } from "./static-pipeline.mjs";

describe("frontend static stream lifecycle", () => {
  it("rejects a source read failure instead of emitting an unhandled error", async () => {
    const failure = Object.assign(new Error("fixture disk read failed"), { code: "EIO" });
    const source = new Readable({
      read() {
        this.destroy(failure);
      },
    });
    const response = new PassThrough();

    await expect(pipeStaticResponse(source, response)).rejects.toBe(failure);
    expect(source.destroyed).toBe(true);
    expect(response.destroyed).toBe(true);
  });

  it("destroys the file stream when the browser disconnects", async () => {
    const source = new PassThrough();
    const response = new PassThrough();
    const completed = pipeStaticResponse(source, response);
    source.write("partial asset");

    response.destroy();
    await completed;

    expect(source.destroyed).toBe(true);
  });
});
