import { fileURLToPath } from "node:url";
import path from "node:path";
import { readFileSync } from "node:fs";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { viteSingleFile } from "vite-plugin-singlefile";
import { themeBootScript } from "@codesweep-ai/ui";

// The viewer must stay one self-contained HTML file: cli.go embeds
// ../internal/cli/shell/viewer.html and the page must open from file:// with
// no network. Everything — JS, CSS, the logo — inlines into dist/index.html,
// which the build script copies over the embed path.
const uiVersion = JSON.parse(readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "node_modules/@codesweep-ai/ui/package.json"), "utf8")).version as string;
export default defineConfig({
  define: { __UI_VERSION__: JSON.stringify(uiVersion) },
  plugins: [
    react(),
    // The pre-paint theme boot script is the package's own, so the boot rule
    // can never drift from useTheme (CP-03). vite.config.ts runs in Node with
    // the app's dependencies; the marker lives in index.html.
    {
      name: "theme-boot",
      transformIndexHtml: {
        order: "pre" as const,
        handler: (html: string) =>
          html.replace(
            "/*THEME-BOOT*/",
            themeBootScript({ storageKey: "dispatch-viewer-theme", urlParam: "theme" }),
          ),
      },
    },
    viteSingleFile(),
  ],
  build: {
    outDir: "dist",
    assetsInlineLimit: 100 * 1024 * 1024,
    chunkSizeWarningLimit: 100 * 1024,
  },
});
