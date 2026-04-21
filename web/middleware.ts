import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

const protectedPrefixes = ["/dashboard", "/groups", "/violations", "/audit"];

export function middleware(request: NextRequest) {
  const { pathname } = request.nextUrl;
  const requiresAuth = protectedPrefixes.some((prefix) => pathname.startsWith(prefix));

  if (!requiresAuth) {
    return NextResponse.next();
  }

  const token = request.cookies.get("cg_admin")?.value;
  if (!token) {
    return NextResponse.redirect(new URL("/", request.url));
  }

  return NextResponse.next();
}

export const config = {
  matcher: ["/dashboard/:path*", "/groups/:path*", "/violations/:path*", "/audit/:path*"],
};
