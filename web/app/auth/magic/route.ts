import { NextRequest, NextResponse } from "next/server";

function publicURL(path: string, req: NextRequest): URL {
  const base =
    process.env.PUBLIC_BASE_URL ||
    process.env.NEXT_PUBLIC_BASE_URL ||
    (() => {
      const proto = req.headers.get("x-forwarded-proto") || "https";
      const host = req.headers.get("x-forwarded-host") || req.headers.get("host");
      if (host) return `${proto}://${host}`;
      return null;
    })();
  if (base) {
    return new URL(path, base);
  }
  return new URL(path, req.url);
}

// Legacy plaintext magic links are retired so a leaked URL or BOT_TOKEN can never mint an admin session.
export async function GET(req: NextRequest) {
  return NextResponse.redirect(publicURL("/?error=magic_disabled", req));
}
