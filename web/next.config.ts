import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  async headers() {
    const miniHeaders = [{ key: "Content-Security-Policy", value: "frame-ancestors 'self' https://web.telegram.org" }, { key: "Referrer-Policy", value: "no-referrer" }, { key: "Cache-Control", value: "no-store" }];
    return [{ source: "/verify/mini", headers: miniHeaders }, { source: "/appeal/mini", headers: miniHeaders }];
  },
};

export default nextConfig;
