"use client";

import { useEffect, useState } from "react";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardHeader, CardTitle, CardBody } from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { apiFetch } from "@/lib/api";
import { useToast } from "@/components/providers";
import { Save, Play } from "lucide-react";

export default function PromptEditorPage() {
  const { pushToast } = useToast();
  const [configState, setConfigState] = useState<Record<string, unknown>>({});
  const [customRules, setCustomRules] = useState("");
  const [original, setOriginal] = useState("");
  const [testText, setTestText] = useState("");
  const [result, setResult] = useState("");
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);

  useEffect(() => {
    apiFetch<{
      config: Record<string, unknown> & { ai?: { custom_rules?: string } };
    }>("/api/admin/global-config")
      .then((p) => {
        setConfigState(p.config ?? {});
        const rules = p.config?.ai?.custom_rules ?? "";
        setCustomRules(rules);
        setOriginal(rules);
      })
      .catch((e) =>
        pushToast(e instanceof Error ? e.message : "加载失败", "error"),
      );
  }, [pushToast]);

  const dirty = customRules !== original;

  async function save() {
    setSaving(true);
    try {
      await apiFetch("/api/admin/global-config", {
        method: "PUT",
        body: JSON.stringify({
          ...configState,
          ai: {
            ...((configState.ai as Record<string, unknown> | undefined) ?? {}),
            custom_rules: customRules,
          },
        }),
      });
      setOriginal(customRules);
      pushToast("已保存", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  }

  async function test() {
    if (!testText.trim()) {
      pushToast("测试消息不能为空", "error");
      return;
    }
    setTesting(true);
    try {
      const p = await apiFetch<{ result: unknown }>("/api/admin/ai-test", {
        method: "POST",
        body: JSON.stringify({ text: testText }),
      });
      setResult(JSON.stringify(p.result, null, 2));
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "测试失败", "error");
    } finally {
      setTesting(false);
    }
  }

  return (
    <AdminShell
      title="Prompt 编辑器"
      subtitle="管理 AI 审核的自定义规则（system prompt 骨架固定，只能追加规则）"
    >
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader className="flex-row items-center justify-between">
            <CardTitle>自定义规则</CardTitle>
            {dirty && (
              <span className="text-xs text-[var(--warning)]">· 未保存</span>
            )}
          </CardHeader>
          <CardBody className="space-y-3">
            <Textarea
              value={customRules}
              onChange={(e) => setCustomRules(e.target.value)}
              className="min-h-[320px] font-mono text-xs leading-relaxed"
              placeholder="在此追加自定义规则，将拼接到固定 system prompt 末尾&#10;&#10;例如：&#10;- 本群禁止讨论币圈、刷单、招聘&#10;- 本群禁止所有链接&#10;- 宽松程度：对娱乐内容从宽处理"
            />
            <div className="flex justify-end">
              <Button onClick={save} disabled={saving || !dirty}>
                <Save className="h-3.5 w-3.5" />
                {saving ? "保存中…" : "保存"}
              </Button>
            </div>
          </CardBody>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>实时测试</CardTitle>
          </CardHeader>
          <CardBody className="space-y-3">
            <Input
              value={testText}
              onChange={(e) => setTestText(e.target.value)}
              placeholder="输入一条模拟消息"
            />
            <Button onClick={test} disabled={testing} variant="secondary">
              <Play className="h-3.5 w-3.5" />
              {testing ? "调用中…" : "调用 AI 测试"}
            </Button>
            <pre className="min-h-[280px] overflow-x-auto rounded-md border border-[var(--border)] bg-[var(--surface-2)] p-3 text-xs leading-relaxed">
              {result || "结果将显示在这里"}
            </pre>
          </CardBody>
        </Card>
      </div>
    </AdminShell>
  );
}
