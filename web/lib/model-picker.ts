import { apiFetch } from "@/lib/api";

type PickerProvider = { name?: string; models?: Array<{ key?: string; label?: string }> | null };

// This endpoint intentionally exposes only model-picker metadata to scoped admins.
export async function fetchModelOptions(): Promise<Array<{ value: string; label: string }>> {
  const payload = await apiFetch<{ providers?: PickerProvider[] }>("/api/admin/ai-models");
  return (payload.providers ?? []).flatMap((provider) => (provider.models ?? []).flatMap((model) => {
    const providerKey = (provider.name ?? "").trim();
    const modelKey = (model.key ?? "").trim();
    if (!providerKey || !modelKey) return [];
    const value = `${providerKey}:${modelKey}`;
    return [{ value, label: `${value} · ${model.label || modelKey}` }];
  }));
}
