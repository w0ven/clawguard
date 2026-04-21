"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";

const COLLAPSE_THRESHOLD = 500;
const PREVIEW_LENGTH = 200;

export function MessageTextBlock({
  text,
  emptyLabel = "无原文",
}: {
  text: string | null | undefined;
  emptyLabel?: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const value = text ?? "";
  if (!value) {
    return <div className="text-xs text-[var(--text-muted)]">{emptyLabel}</div>;
  }

  const collapsible = value.length > COLLAPSE_THRESHOLD;
  const display = !collapsible || expanded ? value : `${value.slice(0, PREVIEW_LENGTH)}...`;

  return (
    <div className="space-y-2">
      <pre className="max-h-56 overflow-auto rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-3 font-mono text-xs leading-5 whitespace-pre-wrap break-words text-[var(--text)]">
        {display}
      </pre>
      {collapsible ? (
        <Button
          type="button"
          size="sm"
          variant="ghost"
          onClick={() => setExpanded((v) => !v)}
        >
          {expanded ? "收起" : "展开"}
        </Button>
      ) : null}
    </div>
  );
}
