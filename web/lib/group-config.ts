// The API owns the revision token; retain its exact JSON type and value.
export type GroupConfigRevision = string | number;

export function isGroupConfigRevision(value: unknown): value is GroupConfigRevision {
  return (typeof value === "string" && value.length > 0) ||
    (typeof value === "number" && Number.isSafeInteger(value));
}
