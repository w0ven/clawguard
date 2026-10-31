"use client";

import { useEffect, useState } from "react";
import { DualCaptchaVerifyClient } from "@/components/dual-captcha-verify-client";

export default function DualVerifyPage() {
  const [token, setToken] = useState<string | null>(null);
  useEffect(() => { setToken(window.location.hash.slice(1)); }, []);
  if (token === null) return <main className="p-6 text-center">正在加载验证…</main>;
  if (!token) return <main className="p-6 text-center">验证链接无效，请返回群组联系管理员。</main>;
  return <DualCaptchaVerifyClient token={token} />;
}
