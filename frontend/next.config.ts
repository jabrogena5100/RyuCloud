import type { NextConfig } from "next";

// The browser only ever talks to this Next.js server. Requests to /api/v1/* are
// proxied to the Go API, so the session cookie is first-party and no CORS is needed.
// Rewrites are resolved when the server starts (dev) or at build time (prod).
const apiProxyTarget = process.env.API_PROXY_TARGET ?? "http://localhost:8000";

const securityHeaders = [
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
];

const nextConfig: NextConfig = {
  output: "standalone",
  poweredByHeader: false,
  async rewrites() {
    return [{ source: "/api/v1/:path*", destination: `${apiProxyTarget}/api/v1/:path*` }];
  },
  async headers() {
    return [{ source: "/:path*", headers: securityHeaders }];
  },
};

export default nextConfig;
