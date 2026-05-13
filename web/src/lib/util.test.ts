// @vitest-environment happy-dom

import { describe, expect, it } from "vitest";

import { html, raw, setHTML } from "./util";

describe("setHTML", () => {
  it("removes executable elements and unsafe URL attributes", () => {
    const root = document.createElement("div");

    setHTML(root, html`
      <script>window.evil = true</script>
      <a id="js" href="javascript:alert(1)">bad</a>
      <iframe srcdoc="<p>bad</p>"></iframe>
      <img id="data-html" src="data:text/html,<script>alert(1)</script>">
      <img id="data-image" src="data:image/png;base64,AAAA">
    `);

    expect(root.querySelector("script")).toBeNull();
    expect(root.querySelector("iframe")).toBeNull();
    expect(root.querySelector<HTMLAnchorElement>("#js")?.hasAttribute("href")).toBe(false);
    expect(root.querySelector<HTMLImageElement>("#data-html")?.hasAttribute("src")).toBe(false);
    expect(root.querySelector<HTMLImageElement>("#data-image")?.getAttribute("src")).toBe("data:image/png;base64,AAAA");
  });
});

describe("html tagged template", () => {
  it("escapes interpolated values by default", () => {
    const root = document.createElement("div");
    const userName = `<img src=x onerror="alert(1)">`;

    setHTML(root, html`<p id="name">${userName}</p>`);

    expect(root.querySelector("img")).toBeNull();
    expect(root.querySelector("#name")?.textContent).toBe(userName);
  });

  it("passes nested html`` fragments and arrays through unescaped", () => {
    const root = document.createElement("div");
    const rows = ["a & b", "c < d"].map((label) => html`<li>${label}</li>`);

    setHTML(root, html`<ul>${rows}</ul>`);

    const items = root.querySelectorAll("li");
    expect(items).toHaveLength(2);
    expect(items[0]?.textContent).toBe("a & b");
    expect(items[1]?.textContent).toBe("c < d");
  });

  it("only emits raw() markup verbatim", () => {
    const root = document.createElement("div");
    const icon = raw(`<svg><path d="M0 0"></path></svg>`);

    setHTML(root, html`<span class="icon">${icon}</span>`);

    expect(root.querySelector("svg path")).not.toBeNull();
  });
});
