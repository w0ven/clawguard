"use client";

import { useEffect, useState } from "react";
import { GuardedLink } from "@/components/guarded-link";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@/components/ui/table";
import { apiFetch } from "@/lib/api";
import type { Admin, Group } from "@/lib/types";
import { useToast } from "@/components/providers";
import { Settings, ChevronRight } from "lucide-react";

export default function GroupsPage() {
  const { pushToast } = useToast();
  const [groups, setGroups] = useState<Group[]>([]);
  const [me, setMe] = useState<Admin | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    Promise.all([
      apiFetch<{ groups: Group[] }>("/api/admin/groups"),
      apiFetch<{ admin: Admin }>("/api/auth/me"),
    ])
      .then(([g, m]) => {
        setGroups(g.groups ?? []);
        setMe(m.admin ?? null);
      })
      .catch((e) =>
        pushToast(e instanceof Error ? e.message : "加载失败", "error"),
      )
      .finally(() => setLoading(false));
  }, [pushToast]);

  const canEditGlobal =
    me?.role === "owner" || (me?.group_scope?.length ?? 0) === 0;

  return (
    <AdminShell
      title="群管理"
      subtitle="查看 Bot 管理的群组"
      actions={
        canEditGlobal ? (
          <GuardedLink href="/groups/global-config">
            <Button variant="secondary">
              <Settings className="h-3.5 w-3.5" />
              全局策略
            </Button>
          </GuardedLink>
        ) : null
      }
    >
      <Card>
        <CardHeader>
          <CardTitle>群组列表 ({groups.length})</CardTitle>
        </CardHeader>
        {groups.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "暂无群组 — 把 Bot 加入群并设为管理员即可接管"}
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeaderCell>群名</TableHeaderCell>
                <TableHeaderCell>Chat ID</TableHeaderCell>
                <TableHeaderCell>类型</TableHeaderCell>
                <TableHeaderCell>成员</TableHeaderCell>
                <TableHeaderCell>状态</TableHeaderCell>
                <TableHeaderCell></TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {groups.map((g) => (
                <TableRow key={g.chat_id}>
                  <TableCell>
                    <GuardedLink
                      href={`/groups/${g.chat_id}`}
                      className="font-medium text-[var(--accent)] hover:underline"
                    >
                      {g.title}
                    </GuardedLink>
                  </TableCell>
                  <TableCell className="tabular-nums text-[var(--text-muted)]">
                    {g.chat_id}
                  </TableCell>
                  <TableCell className="text-[var(--text-muted)]">
                    {g.type}
                  </TableCell>
                  <TableCell className="tabular-nums">{g.member_count}</TableCell>
                  <TableCell>
                    <Badge tone={g.enabled ? "success" : "default"}>
                      {g.enabled ? "启用" : "停用"}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <GuardedLink
                      href={`/groups/${g.chat_id}`}
                      className="text-[var(--text-muted)] hover:text-[var(--text)]"
                    >
                      <ChevronRight className="h-4 w-4" />
                    </GuardedLink>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Card>
    </AdminShell>
  );
}
