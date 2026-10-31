"use client";

import { useEffect, useState } from "react";

export function DualCaptchaAvailability() {
  const [available, setAvailable] = useState<boolean | null>(null);
  useEffect(() => {
    let active = true;
    fetch("/api/verify/capabilities", { cache: "no-store" })
      .then((response) => {
        if (!response.ok) throw new Error("unavailable");
        return response.json() as Promise<{ dual_captcha_available?: boolean }>;
      })
      .then((body) => { if (active) setAvailable(body.dual_captcha_available === true); })
      .catch(() => { if (active) setAvailable(false); });
    return () => { active = false; };
  }, []);
  return <p className="mt-1 text-xs text-[var(--text-muted)]">Turnstile 与 hCaptcha 均需通过。{available === null ? "正在检查配置…" : available ? "双重验证已配置。" : "双重验证不可用：请先配置两家密钥；未配置时不能保存此方式。"}</p>;
}
