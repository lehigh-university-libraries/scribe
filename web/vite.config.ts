import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
      "mirador-scribe": path.resolve(__dirname, "../mirador-scribe/src/index.js"),
      react: path.resolve(__dirname, "node_modules/react"),
      "react-dom": path.resolve(__dirname, "node_modules/react-dom"),
      "@emotion/react": path.resolve(__dirname, "node_modules/@emotion/react"),
      "@emotion/styled": path.resolve(__dirname, "node_modules/@emotion/styled"),
      "@mui/icons-material": path.resolve(__dirname, "node_modules/@mui/icons-material"),
      "@mui/material": path.resolve(__dirname, "node_modules/@mui/material"),
      "@mui/system": path.resolve(__dirname, "node_modules/@mui/system"),
      i18next: path.resolve(__dirname, "node_modules/i18next"),
      mirador: path.resolve(__dirname, "node_modules/mirador"),
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
        target: "http://localhost:8080",
        changeOrigin: true
      },
      "/scribe.v1.": {
        target: "http://localhost:8080",
        changeOrigin: true
      },
      "/auth": {
        target: "http://localhost:8080",
        changeOrigin: true
      },
      "/logout": {
        target: "http://localhost:8080",
        changeOrigin: true
      },
      "/static/uploads": {
        target: "http://localhost:8080",
        changeOrigin: true
      },
      "/iiif": {
        target: "http://localhost:8081",
        changeOrigin: true
      },
      "/healthz": {
        target: "http://localhost:8080",
        changeOrigin: true
      }
    }
  },
  build: {
    // Mirador is loaded only by the editor route; keep the accepted lazy route
    // budget explicit so unrelated bundle growth still trips this check.
    chunkSizeWarningLimit: 1500,
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
