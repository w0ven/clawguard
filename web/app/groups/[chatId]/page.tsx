"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { AdminShell } from "@/components/admin-shell";
import { GroupConfigEditor } from "@/components/group-config-editor";
import { GuardedLink } from "@/components/guarded-link";
import { Button } from "@/components/ui/button";
import { Card, CardBody } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { apiFetch } from "@/lib/api";
import type { GuardPolicy, Group } from "@/lib/types";
import { useToast } from "@/components/providers";

export default function GroupDetailPage() {
  const params = useParams<{ chatId: string }>();
  const { pushToast } = useToast();
  const [group, setGroup] = useState<Group | null>(null);
  const [mergedPolicy, setMergedPolicy] = useState<GuardPolicy | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setGroup(null);
    setMergedPolicy(null);
    setLoadError(null);

    apiFetch<{ group: Group; merged_policy: GuardPolicy }>(`/api/admin/groups/${params.chatId}`)
      .then((payload) => {
        if (!alive) return;
        setGroup(payload.group);
        setMergedPolicy(payload.merged_policy);
      })
      .catch((error) => {
        if (!alive) return;
        const message = error instanceof Error ? error.message : "加载群详情失败";
        setLoadError(message);
        pushToast(message, "error");
      })
      .finally(() => {
        if (alive) setLoading(false);
      });

    return () => {
      alive = false;
    };
  }, [params.chatId, pushToast]);

  return (
    <AdminShell
      title={group?.title ?? "群详情"}
      subtitle={`Chat ID: ${params.chatId}。可在各策略页按字段选择“继承全局”或写入群级覆盖。`}
      actions={
        <GuardedLink href={`/groups/${params.chatId}/assistant`}>
          <Button type="button" variant="secondary" size="sm">打开群助手</Button>
        </GuardedLink>
      }
    >
      {loading && (
        <Card><CardBody className="flex items-center gap-3 py-12 text-sm text-[var(--text-muted)]"><span className="h-2 w-2 animate-pulse rounded-full bg-[var(--accent)]" />正在加载群策略…</CardBody></Card>
      )}
      {loadError && !loading && (
        <Card><CardBody className="flex flex-col gap-2 py-10 text-sm text-[var(--danger)]"><Badge tone="danger">加载失败</Badge><span>{loadError}</span></CardBody></Card>
      )}
      {group && mergedPolicy && !loadError ? <GroupConfigEditor key={group.chat_id} group={group} mergedPolicy={mergedPolicy} /> : null}
    </AdminShell>
  );
}
