import { createRequire } from "node:module";
import { describe, expect, it } from "vitest";

// The player loads hls.js with a dynamic import() and its test mocks the module, so a missing install
// would pass every other test and only break in the browser ("Module not found: Can't resolve 'hls.js'").
// In Compose the usual cause is a stale node_modules volume: recreate it with
//   docker compose up -d --no-deps --renew-anon-volumes --force-recreate frontend
describe("runtime dependencies", () => {
  it.each(["hls.js"])("%s is installed", (name) => {
    const resolve = createRequire(import.meta.url).resolve;
    expect(() => resolve(name)).not.toThrow();
  });
});
