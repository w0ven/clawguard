import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

const publicPrefixes = ["/auth", "/verify", "/miniapp", "/api"];

function publicOriginURL(path: string, request: NextRequest): URL {
  const envBase = process.env.PUBLIC_BASE_URL || process.env.NEXT_PUBLIC_BASE_URL;
  if (envBase) {
    return new URL(path, envBase);
  }
  const proto =
    request.headers.get("x-forwarded-proto") ||
    request.nextUrl.protocol.replace(/:$/, "") ||
    "https";
  const host = request.headers.get("x-forwarded-host") || request.headers.get("host");
  if (host) {
    return new URL(path, `${proto}://${host}`);
  }
  return new URL(path, request.url);
}

export function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;
  const isPublic =
    pathname === "/" ||
    pathname === "/appeal/mini" ||
    pathname === "/icon.svg" ||
    publicPrefixes.some(
      (prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`),
    );

  if (isPublic) {
    return NextResponse.next();
  }

  const token = request.cookies.get("cg_admin")?.value;
  if (!token) {
    return NextResponse.redirect(publicOriginURL("/", request));
  }

  return NextResponse.next();
}

export const config = {
  matcher: [
    "/((?!_next/static|_next/image|favicon\\.ico|icon\\.svg|.*\\.(?:svg|png|jpg|jpeg|gif|webp|ico|css|js|map|woff|woff2|ttf)$).*)",
  ],
};
