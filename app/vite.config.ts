import { defineConfig } from "vite";
import tailwindcss from "@tailwindcss/vite";
export default defineConfig({
  plugins: [tailwindcss()],
  build: { outDir: "../internal/localapp/dist", emptyOutDir: true, sourcemap: false },
  resolve: { dedupe: ["react", "react-dom"] },
});
