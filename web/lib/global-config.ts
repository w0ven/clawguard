import { ApiError, apiFetch } from "@/lib/api";

export type GlobalConfigSection = "document" | "prompt" | "adkiller" | "llm";

export type GlobalConfigPayload = {
  config: Record<string, unknown>;
  version: number;
  updated_at?: string;
};

export const GLOBAL_CONFIG_CONFLICT_MESSAGE =
  "配置已被其他人更新，未覆盖远端配置。草稿仍在本页；请先复制保留修改，再刷新页面核对最新配置。";

export function isGlobalConfigConflict(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409;
}

export function fetchGlobalConfig() {
  return apiFetch<GlobalConfigPayload>("/api/admin/global-config");
}

export function saveGlobalConfigSection(
  section: Exclude<GlobalConfigSection, "document">,
  config: Record<string, unknown>,
) {
  return apiFetch<GlobalConfigPayload>("/api/admin/global-config", {
    method: "PUT",
    body: JSON.stringify({ section, config }),
  });
}

export function saveGlobalConfigDocument(
  version: number,
  config: Record<string, unknown>,
) {
  return apiFetch<GlobalConfigPayload>("/api/admin/global-config", {
    method: "PUT",
    body: JSON.stringify({
      section: "document",
      version,
      config,
    }),
  });
}
