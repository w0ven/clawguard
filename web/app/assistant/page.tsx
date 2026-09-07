"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { AlertTriangle, ArrowRight, Bot, Loader2, RefreshCw } from "lucide-react";
import { AdminShell } from "@/components/admin-shell";
import { GuardedLink } from "@/components/guarded-link";
import { useDirtyNavigation } from "@/components/dirty-guard";
import { apiFetch, ApiError } from "@/lib/api";
import type { Group } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export default function AssistantSelectPage() {
  const router = useRouter();
  const { confirmNavigation } = useDirtyNavigation();
  const [groups, setGroups] = useState<Group[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ApiError | Error | null>(null);

  const loadGroups = () => {
    setLoading(true);
    setError(null);
    apiFetch<{ groups: Group[] }>("/api/admin/groups")
      .then((result) => setGroups((result.groups ?? []).filter((group) => group.enabled)))
      .catch((reason) => setError(reason instanceof ApiError ? reason : new Error(reason instanceof Error ? reason.message : "群列表加载失败")))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    loadGroups();
  }, []);

  const openGroup = (chatId: number) => {
    if (!confirmNavigation("当前页面有未保存的修改，确定切换群组吗？")) return;
    router.push(`/groups/${encodeURIComponent(String(chatId))}/assistant`);
  };

  return (
    <AdminShell title="群助手" subtitle="选择有实际管理权限的群组；配置与运行数据均来自群助手 API">
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
        <Card>
          <CardHeader><CardTitle>选择群组</CardTitle><CardDescription>列表继承现有管理员 JWT 群 scope；客户端不会替代服务端授权。</CardDescription></CardHeader>
          <CardBody>
            {loading ? <div className="flex items-center justify-center gap-2 py-14 text-sm text-[var(--text-muted)]"><Loader2 className="h-4 w-4 animate-spin text-[var(--accent)]" />正在加载可管理群组…</div> : error ? <div className="flex flex-col gap-3 py-10"><div className="flex items-start gap-3 text-sm"><AlertTriangle className="mt-0.5 h-4 w-4 text-[var(--danger)]" /><div><Badge tone={error instanceof ApiError && error.status === 403 ? "warning" : "danger"}>{error instanceof ApiError && error.status === 403 ? "没有群管理权限" : "无法读取群组"}</Badge><p className="mt-2 text-xs text-[var(--text-muted)]">{error.message}</p></div></div><Button type="button" variant="secondary" size="sm" onClick={loadGroups} className="w-fit"><RefreshCw className="h-3.5 w-3.5" />重试</Button></div> : groups.length === 0 ? <div className="py-10 text-center"><p className="text-sm font-medium">没有可管理群组</p><p className="mt-1 text-xs text-[var(--text-muted)]">服务端返回的群列表为空，不能使用原型中的示例群。</p></div> : <div className="grid gap-3 sm:grid-cols-2">{groups.map((group) => <button type="button" key={group.chat_id} onClick={() => openGroup(group.chat_id)} className="group rounded-2xl border border-[var(--border)] bg-[var(--surface-2)] p-4 text-left transition hover:border-[var(--border-strong)] hover:bg-[var(--surface)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"><div className="flex items-start justify-between gap-3"><div className="flex min-w-0 items-center gap-3"><div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-[var(--accent-soft)] text-[var(--accent)]"><Bot className="h-4 w-4" /></div><div className="min-w-0"><p className="truncate text-sm font-semibold">{group.title}</p><p className="mt-1 truncate font-mono text-[11px] text-[var(--text-muted)]">{group.chat_id}</p></div></div><ArrowRight className="h-4 w-4 shrink-0 text-[var(--text-subtle)] transition group-hover:translate-x-0.5 group-hover:text-[var(--accent)]" /></div><div className="mt-4 flex items-center gap-2"><Badge tone={group.enabled ? "success" : "warning"}>{group.enabled ? "已授权" : "已停用"}</Badge><span className="text-xs text-[var(--text-muted)]">{group.type} · {group.member_count} 成员</span></div></button>)}</div>}
          </CardBody>
        </Card>
        <Card>
          <CardHeader><CardTitle>边界说明</CardTitle><CardDescription>真实生产功能，不是原型模拟器。</CardDescription></CardHeader>
          <CardBody className="space-y-3 text-sm text-[var(--text-muted)]"><p>进入群后只展示实际返回的启用状态、保留政策、模型池状态和记忆来源。</p><p>新群助手默认关闭；聊天和学习必须分别显式启用。</p><p>模型、provider URL/key 不在本页自填；模型引用由现有 registry 和服务端能力校验。</p><GuardedLink href="/groups" className="inline-flex text-xs text-[var(--accent)] hover:underline">查看全部群管理 <ArrowRight className="ml-1 h-3.5 w-3.5" /></GuardedLink></CardBody>
        </Card>
      </div>
    </AdminShell>
  );
}
