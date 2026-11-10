import { NextRequest } from "next/server";
import { redirectToPublicPath } from "@/lib/safe-redirect";

// Legacy plaintext magic links are retired so a leaked URL or BOT_TOKEN can never mint an admin session.
export async function GET(req: NextRequest) {
  return redirectToPublicPath("/?error=magic_disabled", req);
}
