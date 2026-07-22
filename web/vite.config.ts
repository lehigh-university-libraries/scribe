import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import type { ClientRequest, IncomingHttpHeaders } from "node:http";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const devBackendOrigin = process.env.SCRIBE_DEV_BACKEND_ORIGIN || "http://localhost";
const devPresentationOrigin = process.env.SCRIBE_DEV_PRESENTATION_ORIGIN || devBackendOrigin;

const publicPresentationRequestCredentials = [
  "authorization",
  "cookie",
  "x-scribe-api-key",
  "x-scribe-workspace-id",
] as const;

export function stripPublicPresentationRequestCredentials(
  request: Pick<ClientRequest, "removeHeader">,
): void {
  for (const name of publicPresentationRequestCredentials) {
    request.removeHeader(name);
  }
}

export function stripPublicPresentationResponseCredentials(
  headers: IncomingHttpHeaders,
): void {
  for (const name of Object.keys(headers)) {
    if (name.toLowerCase() === "set-cookie") delete headers[name];
  }
}

const chunkBudgets = [
  { prefix: "mirador-", bytes: 1_900_000 },
  { prefix: "mui-", bytes: 900_000 },
  { prefix: "react-vendor-", bytes: 250_000 }
] as const;

function enforceBundleBudgets(): Plugin {
  return {
    name: "scribe-bundle-budgets",
    apply: "build",
    generateBundle(_options, bundle) {
      for (const output of Object.values(bundle)) {
        if (output.type !== "chunk") continue;
        const namedBudget = chunkBudgets.find(({ prefix }) => output.fileName.startsWith(`assets/${prefix}`));
        const budget = namedBudget?.bytes ?? 300_000;
        const bytes = Buffer.byteLength(output.code, "utf8");
        if (bytes > budget) {
          this.error(`${output.fileName} is ${bytes} bytes; the bundle budget is ${budget} bytes`);
        }
      }
    }
  };
}

export default defineConfig({
  plugins: [react(), enforceBundleBudgets()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
      "mirador-scribe": path.resolve(__dirname, "../mirador-scribe/src/index.js"),
      // Do not alias MUI package directories: doing so bypasses MUI 7's
      // conditional exports and forces its CommonJS build into Mirador's ESM.
      // `dedupe` below still gives the linked plugin one dependency instance.
      react: path.resolve(__dirname, "node_modules/react"),
      "react-dom": path.resolve(__dirname, "node_modules/react-dom"),
      "@emotion/react": path.resolve(__dirname, "node_modules/@emotion/react"),
      "@emotion/styled": path.resolve(__dirname, "node_modules/@emotion/styled"),
      i18next: path.resolve(__dirname, "node_modules/i18next"),
      openseadragon: path.resolve(__dirname, "node_modules/openseadragon"),
      "prop-types": path.resolve(__dirname, "node_modules/prop-types"),
      "react-i18next": path.resolve(__dirname, "node_modules/react-i18next")
    },
    dedupe: [
      "react",
      "react-dom",
      "@emotion/react",
      "@emotion/styled",
      "@mui/icons-material",
      "@mui/material",
      "@mui/system",
      "i18next",
      "mirador",
      "openseadragon",
      "prop-types",
      "react-i18next"
    ]
  },
  server: {
    port: 5173,
    fs: {
      allow: [
        __dirname,
        path.resolve(__dirname, "../mirador-scribe/src")
      ]
    },
    proxy: {
      "/v1": {
        target: devBackendOrigin,
        changeOrigin: true
      },
      "/scribe.v1.": {
        target: devBackendOrigin,
        changeOrigin: true
      },
      "/auth": {
        target: devBackendOrigin,
        changeOrigin: true
      },
      "/logout": {
        target: devBackendOrigin,
        changeOrigin: true
      },
      "/static/uploads": {
        target: devBackendOrigin,
        changeOrigin: true
      },
      "/iiif": {
        target: devBackendOrigin,
        changeOrigin: true
      },
      "/presentation": {
        target: devPresentationOrigin,
        changeOrigin: true,
        configure(proxy) {
          // Keep `npm run dev` aligned with the production public-IIIF trust
          // boundary: Presentation must never receive browser credentials or
          // set cookies for the application origin.
          proxy.on("proxyReq", stripPublicPresentationRequestCredentials);
          proxy.on("proxyRes", (response) => {
            stripPublicPresentationResponseCredentials(response.headers);
          });
        }
      },
      "/healthz": {
        target: devBackendOrigin,
        changeOrigin: true
      }
    }
  },
  build: {
    // Mirador is loaded only by the editor route. The plugin above turns these
    // explicit route/chunk budgets into build failures; this warning remains a
    // useful local signal before a budget is exceeded.
    chunkSizeWarningLimit: 1800,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules") && !id.includes("mirador-scribe")) return undefined;
          if (id.includes("mirador") || id.includes("mirador-scribe") || id.includes("openseadragon")) {
            return "mirador";
          }
          if (id.includes("@mui") || id.includes("@emotion") || id.includes("prop-types")) {
            return "mui";
          }
          if (id.includes("/react/") || id.includes("/react-dom/") || id.endsWith("/react/index.js") || id.endsWith("/react-dom/index.js")) {
            return "react-vendor";
          }
          return undefined;
        }
      }
    }
  }
});
