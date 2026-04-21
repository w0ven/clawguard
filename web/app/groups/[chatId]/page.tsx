"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { AdminShell } from "@/components/admin-shell";
import { GroupConfigEditor } from "@/components/group-config-editor";
import { apiFetch } from "@/lib/api";
import type { GuardPolicy, Group } from "@/lib/types";
import { useToast } from "@/components/providers";

export default function GroupDetailPage() {
  const params = useParams<{ chatId: string }>();
  const { pushToast } = useToast();
  const [group, setGroup] = useState<Group | null>(null);
  const [mergedPolicy, setMergedPolicy] = useState<GuardPolicy | null>(null);

  useEffect(() => {
    apiFetch<{ group: Group; merged_policy: GuardPolicy }>(`/api/admin/groups/${params.chatId}`)
      .then((payload) => {
        setGroup(payload.group);
        setMergedPolicy(payload.merged_policy);
      })
      .catch((error) => pushToast(error instanceof Error ? error.message : "加载群详情失败", "error"));
  }, [params.chatId, pushToast]);

  return (
    <AdminShell
      title={group?.title ?? "群详情"}
      subtitle={`Chat ID: ${params.chatId}。可在各策略页按字段选择“继承全局”或写入群级覆盖。`}
    >
      {group && mergedPolicy ? <GroupConfigEditor group={group} mergedPolicy={mergedPolicy} /> : null}
    </AdminShell>
  );
}
