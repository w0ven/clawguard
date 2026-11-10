import { NextRequest, NextResponse } from "next/server";

function safeRelativePath(path: string): string {
  const trimmed = path.trim();
  if (!trimmed.startsWith("/") || trimmed.startsWith("//") || trimmed.includes("\\")) {
    return "/";
  }
  return trimmed;
}

/**
 * Builds an absolute redirect required by Next.js Proxy/Route runtimes without ever
 * trusting client-spoofable X-Forwarded-Host / X-Forwarded-Proto headers:
 * - When PUBLIC_BASE_URL or NEXT_PUBLIC_BASE_URL is configured with an http(s) origin,
 *   resolves the target path strictly against that pinned canonical origin.
 * - Otherwise, resolves against req.nextUrl.origin (ignoring X-Forwarded-Host).
 */
export function redirectToPublicPath(path: string, req: NextRequest, status = 307): NextResponse {
  const safePath = safeRelativePath(path);
  const configuredBase = (
    process.env.PUBLIC_BASE_URL ||
    process.env.NEXT_PUBLIC_BASE_URL ||
    ""
  ).trim();

  if (configuredBase) {
    try {
      const parsed = new URL(configuredBase);
      if (
        (parsed.protocol === "http:" || parsed.protocol === "https:") &&
        parsed.username === "" &&
        parsed.password === ""
      ) {
        return NextResponse.redirect(new URL(safePath, parsed.origin), status);
      }
    } catch {
      // Fall back to req.nextUrl.origin below if env value is malformed.
    }
  }

  return NextResponse.redirect(new URL(safePath, req.nextUrl.origin), status);
}
