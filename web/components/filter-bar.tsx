"use client";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import type { Group } from "@/lib/types";

type Props = {
  groups: Group[];
  values: Record<string, string>;
  onChange: (key: string, value: string) => void;
  onApply: (overrides?: Record<string, string>) => void;
  onReset: () => void;
  mode: "violations" | "ai";
};

function resolveRangeParams(range: string): Record<string, string> {
  const now = new Date();
  let since: Date | null = null;

  if (range === "today") {
    since = new Date(now);
    since.setHours(0, 0, 0, 0);
  } else if (range === "7d") {
    since = new Date(now);
    since.setDate(since.getDate() - 7);
  } else if (range === "30d") {
    since = new Date(now);
    since.setDate(since.getDate() - 30);
  }

  if (!since) {
    return { since: "", until: "" };
  }

  return {
    since: since.toISOString(),
    until: now.toISOString(),
  };
}

export function FilterBar({ groups, values, onChange, onApply, onReset, mode }: Props) {
  function handleApply() {
    onApply({
      ...resolveRangeParams(values.range),
      range: values.range,
    });
  }

  return (
    <div className="grid gap-3 md:grid-cols-3 xl:grid-cols-6">
      <Select
        value={values.range}
        onChange={(e) => onChange("range", e.target.value)}
        options={[
          { label: "今天", value: "today" },
          { label: "7 天", value: "7d" },
          { label: "30 天", value: "30d" },
          { label: "自定义", value: "custom" },
        ]}
      />
      <Select value={values.chat_id} onChange={(e) => onChange("chat_id", e.target.value)}>
        <option value="">全部群</option>
        {groups.map((group) => (
          <option key={group.chat_id} value={String(group.chat_id)}>
            {group.title}
          </option>
        ))}
      </Select>
      <Input
        placeholder="user_id"
        value={values.user_id}
        onChange={(e) => onChange("user_id", e.target.value)}
      />
      {mode === "violations" ? (
        <Input
          placeholder="rule"
          value={values.rule}
          onChange={(e) => onChange("rule", e.target.value)}
        />
      ) : (
        <Select value={values.verdict} onChange={(e) => onChange("verdict", e.target.value)}>
          <option value="">全部判定</option>
          <option value="clean">正常 (normal)</option>
          <option value="ad">广告 (ad)</option>
          <option value="scam">诈骗 (scam)</option>
          <option value="spam">垃圾消息 (spam)</option>
          <option value="harass">骚扰 (harass)</option>
          <option value="porn">色情 (porn)</option>
          <option value="violence">暴力 (violence)</option>
        </Select>
      )}
      <Input
        placeholder={mode === "violations" ? "action" : "category"}
        value={mode === "violations" ? values.action : values.category}
        onChange={(e) => onChange(mode === "violations" ? "action" : "category", e.target.value)}
      />
      <div className="flex gap-2">
        <Button variant="secondary" onClick={handleApply}>应用</Button>
        <Button variant="ghost" onClick={onReset}>重置</Button>
      </div>
    </div>
  );
}
