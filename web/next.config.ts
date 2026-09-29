import type { NextConfig } from "next";

// Production builds are a static export embedded into the Go binary. In
// development `next dev` proxies /api to the Go server (rewrites are not
// available with output: "export", so they are dev-only).
const isDev = process.env.NODE_ENV !== "production";
const api = process.env.BELLINGUA_API ?? "http://127.0.0.1:7070";

const nextConfig: NextConfig = {
  reactCompiler: true,
  ...(isDev
    ? { rewrites: async () => [{ source: "/api/:path*", destination: `${api}/api/:path*` }] }
    : { output: "export", images: { unoptimized: true } }),
};

export default nextConfig;
