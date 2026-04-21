import type { ProfileCheckLog } from "@/lib/types";

export class ApiError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export async function apiFetch<T>(
  input: string,
  init?: RequestInit,
): Promise<T> {
  const response = await fetch(input, {
    ...init,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });

  if (response.status === 401) {
    if (typeof window !== "undefined" && window.location.pathname !== "/") {
      window.location.href = "/";
    }
    throw new ApiError("Unauthorized", response.status);
  }

  if (!response.ok) {
    let message = `Request failed with ${response.status}`;
    try {
      const payload = (await response.json()) as { error?: string };
      if (payload.error) {
        message = payload.error;
      }
    } catch {}
    throw new ApiError(message, response.status);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return (await response.json()) as T;
}

type FetchProfileCheckLogsParams = {
  page?: number;
  pageSize?: number;
  search?: string;
  result?: string;
  mode?: string;
};

export function fetchProfileCheckLogs(
  chatId: number,
  params: FetchProfileCheckLogsParams = {},
) {
  const query = new URLSearchParams({
    chat_id: String(chatId),
    page: String(params.page ?? 1),
    page_size: String(params.pageSize ?? 20),
  });
  if (params.search?.trim()) {
    query.set("search", params.search.trim());
  }
  if (params.result?.trim()) {
    query.set("result", params.result.trim());
  }
  if (params.mode?.trim()) {
    query.set("mode", params.mode.trim());
  }
  return apiFetch<{ items: ProfileCheckLog[]; total: number }>(
    `/api/admin/profile-check-logs?${query.toString()}`,
  );
}

export function deleteOldProfileCheckLogs(days: number) {
  const query = new URLSearchParams({ days: String(days) });
  return apiFetch<{ deleted: number; days: number }>(
    `/api/admin/profile-check-logs?${query.toString()}`,
    {
      method: "DELETE",
    },
  );
}
