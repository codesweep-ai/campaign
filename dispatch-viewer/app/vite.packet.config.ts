import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { viteSingleFile } from "vite-plugin-singlefile";
import { themeBootScript } from "@codesweep-ai/ui";

// The review page `cs-campaign packet` writes. Like the viewer it must stay one
// self-contained file that opens from file:// with no network: internal/cli
// embeds the copy the build script makes of dist/packet/packet.html.
export default defineConfig({
  plugins: [
    react(),
    {
      name: "theme-boot",
      transformIndexHtml: {
        order: "pre" as const,
        handler: (html: string) =>
          html.replace("/*THEME-BOOT*/", themeBootScript({ storageKey: "cs-campaign-packet-theme", urlParam: "theme" })),
      },
    },
    viteSingleFile(),
  ],
  build: {
    outDir: "dist/packet",
    rollupOptions: { input: "packet.html" },
    assetsInlineLimit: 100 * 1024 * 1024,
    chunkSizeWarningLimit: 100 * 1024,
  },
});
