import { NextRequest, NextResponse } from "next/server";

const cookieMaxAge = 24 * 3600;

function publicURL(path: string, req: NextRequest): URL {
  // 优先用 PUBLIC_BASE_URL/NEXT_PUBLIC_BASE_URL（避免容器内 hostname 泄漏）
  const base =
    process.env.PUBLIC_BASE_URL ||
    process.env.NEXT_PUBLIC_BASE_URL ||
    // 退化到请求头 x-forwarded-*（Caddy/CF 应设置）
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

export async function GET(req: NextRequest) {
  const token = req.nextUrl.searchParams.get("token");
  if (!token) {
    return NextResponse.redirect(publicURL("/?error=missing_token", req));
  }

  const res = await fetch(
    `${process.env.BOT_INTERNAL_URL || "http://bot:8080"}/api/public/magic/exchange`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: token.trim() }),
      cache: "no-store",
    },
  );

  if (!res.ok) {
    return NextResponse.redirect(publicURL("/?error=invalid_token", req));
  }

  const data = (await res.json()) as { jwt?: string; csrf_token?: string; redirect_to?: string };
  if (!data.jwt) {
    return NextResponse.redirect(publicURL("/?error=invalid_token", req));
  }

  const response = NextResponse.redirect(
    publicURL(data.redirect_to || "/dashboard", req),
  );
  response.cookies.set("cg_admin", data.jwt, {
    httpOnly: true,
    secure: true,
    sameSite: "strict",
    maxAge: cookieMaxAge,
    path: "/",
  });
  if (data.csrf_token) {
    response.cookies.set("cg_csrf", data.csrf_token, {
      httpOnly: false,
      secure: true,
      sameSite: "strict",
      maxAge: cookieMaxAge,
      path: "/",
    });
  }
  return response;
}
