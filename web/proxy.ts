import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";
import { redirectToPublicPath } from "@/lib/safe-redirect";

const publicPrefixes = ["/auth", "/verify", "/miniapp", "/api"];

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
    return redirectToPublicPath("/", request);
  }

  return NextResponse.next();
}

export const config = {
  matcher: [
    "/((?!_next/static|_next/image|favicon\\.ico|icon\\.svg|.*\\.(?:svg|png|jpg|jpeg|gif|webp|ico|css|js|map|woff|woff2|ttf)$).*)",
  ],
};
