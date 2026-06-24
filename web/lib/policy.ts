export function getPathValue(source: unknown, path: string[]) {
  return path.reduce<unknown>((current, key) => {
    if (!current || typeof current !== "object") {
      return undefined;
    }
    return (current as Record<string, unknown>)[key];
  }, source);
}

export function hasPath(source: unknown, path: string[]) {
  let current = source;
  for (const key of path) {
    if (!current || typeof current !== "object" || !(key in (current as Record<string, unknown>))) {
      return false;
    }
    current = (current as Record<string, unknown>)[key];
  }
  return true;
}

export function setPath(target: Record<string, unknown>, path: string[], value: unknown) {
  let current = target;
  for (let index = 0; index < path.length - 1; index += 1) {
    const key = path[index];
    const next = current[key];
    if (!next || typeof next !== "object" || Array.isArray(next)) {
      current[key] = {};
    }
    current = current[key] as Record<string, unknown>;
  }
  current[path[path.length - 1]] = value;
}

export function formatArrayValue(value: unknown) {
  if (!Array.isArray(value)) {
    return "";
  }
  return value.join("\n");
}

export function parseArrayValue(value: string, splitComma = true) {
  // 同时支持换行、半角逗号、全角逗号分隔（方便手机端输入）
  // 正则模式下只按换行分隔，避免正则中的逗号被拆断
  const sep = splitComma ? /[\n,，]/ : /\n/;
  return value
    .split(sep)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function cleanStaleAIModelRefs(
  config: Record<string, unknown>,
  modelOptions: Array<{ value: string }>,
) {
  if (modelOptions.length === 0) {
    return config;
  }
  const allowed = new Set(modelOptions.map((option) => option.value));
  const aiConfig = config.ai;
  if (!aiConfig || typeof aiConfig !== "object" || Array.isArray(aiConfig)) {
    return config;
  }

  const currentAI = aiConfig as Record<string, unknown>;
  let changed = false;
  const next = structuredClone(config) as Record<string, unknown>;
  const nextAI = next.ai as Record<string, unknown>;

  const primary = currentAI.primary_model_ref;
  if (typeof primary === "string") {
    const trimmed = primary.trim();
    if (trimmed !== "" && allowed.has(trimmed) && trimmed !== primary) {
      nextAI.primary_model_ref = trimmed;
      changed = true;
    } else if (trimmed !== "" && !allowed.has(trimmed)) {
      delete nextAI.primary_model_ref;
      changed = true;
    }
  }

  const fallback = currentAI.fallback_model_refs;
  if (Array.isArray(fallback)) {
    const filtered = fallback
      .filter((item): item is string => typeof item === "string")
      .map((item) => item.trim())
      .filter((item) => item !== "" && allowed.has(item));
    if (
      filtered.length !== fallback.length ||
      filtered.some((item, index) => item !== fallback[index])
    ) {
      nextAI.fallback_model_refs = filtered;
      changed = true;
    }
  }

  return changed ? next : config;
}
