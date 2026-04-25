export function validateHttpURL(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "";

  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return "按钮链接必须是完整 URL";
  }

  if (!parsed.hostname) return "按钮链接必须是完整 URL";
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return `按钮链接不支持 ${parsed.protocol.replace(/:$/, "://")}，仅支持 http:// 或 https://`;
  }
  return "";
}
