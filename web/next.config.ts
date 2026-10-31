import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  async headers() {
    return [{ source: "/verify/mini", headers: [{ key: "Content-Security-Policy", value: "frame-ancestors 'self' https://web.telegram.org" }] }];
  },
};

export default nextConfig;
