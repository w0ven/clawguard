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
