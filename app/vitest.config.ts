import { defineConfig } from "vitest/config";
export default defineConfig({
  resolve: { dedupe: ["react", "react-dom"] },
  test: { environment: "jsdom", maxWorkers: 2, include: ["src/**/*.test.{ts,tsx}"] },
});
