import { defineConfig } from "vitest/config";

// The formatters follow the runtime locale on purpose, so the expected strings
// in the tests only hold for one. Workers read it when they start, after this
// file runs, so every machine formats as en-US.
process.env.LANG = "en_US.UTF-8";
process.env.LC_ALL = "en_US.UTF-8";

export default defineConfig({
  test: {
    // More workers make the DOM stress tests compete for resources.
    maxWorkers: 2,
    environment: "node",
    include: ["src/**/*.test.{ts,tsx}", "app/**/*.test.{ts,tsx}", "gallery/**/*.test.ts"],
    coverage: {
      provider: "v8",
      reporter: ["text", "html"],
      reportsDirectory: "coverage",
      include: ["src/**/*.{ts,tsx}"],
      exclude: ["src/**/*.test.{ts,tsx}"],
    },
  },
});
