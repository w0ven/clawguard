"use client";

import { useEffect, useRef, useState } from "react";
import { AdminShell } from "@/components/admin-shell";
import { useDirtyGuard } from "@/components/dirty-guard";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@/components/ui/table";
import { apiFetch } from "@/lib/api";
import type { AuthorizedGroup } from "@/lib/types";
import { useToast } from "@/components/providers";
import { Plus, Trash2 } from "lucide-react";

function formatTime(ts: string) {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "-";
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export default function AuthorizedGroupsPage() {
  const { pushToast } = useToast();
  const [groups, setGroups] = useState<AuthorizedGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [chatId, setChatId] = useState("");
  const [title, setTitle] = useState("");
  const savedTitles = useRef(new Map<number, string>());
  const actionLock = useRef(false);
  const hasDraft = groups.some((group) => group.title !== savedTitles.current.get(group.chat_id));
  useDirtyGuard(hasDraft, "群标题尚未保存，确定离开吗？");

  async function load() {
    setLoading(true);
    try {
      const res = await apiFetch<{ groups: AuthorizedGroup[] }>(
        "/api/admin/authorized-groups",
      );
      const items = res.groups ?? [];
      savedTitles.current = new Map(items.map((group) => [group.chat_id, group.title]));
      setGroups(items);
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  async function createGroup() {
    if (actionLock.current) return;
    if (!chatId.trim()) {
      pushToast("chat_id 不能为空", "error");
      return;
    }
    actionLock.current = true;
    setSaving(true);
    try {
      await apiFetch("/api/admin/authorized-groups", {
        method: "POST",
        body: JSON.stringify({
          chat_id: Number(chatId),
          title: title.trim() || null,
        }),
      });
      setChatId("");
      setTitle("");
      await load();
      pushToast("已添加授权群", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "保存失败", "error");
    } finally {
      actionLock.current = false;
      setSaving(false);
    }
  }

  async function updateGroup(group: AuthorizedGroup, patch: Partial<AuthorizedGroup>) {
    if (actionLock.current) return;
    actionLock.current = true;
    setSaving(true);
    try {
      await apiFetch(`/api/admin/authorized-groups/${group.chat_id}`, {
        method: "PUT",
        body: JSON.stringify({
          title: patch.title ?? group.title,
          enabled: patch.enabled ?? group.enabled,
          notes: patch.notes ?? group.notes,
        }),
      });
      await load();
      pushToast("已更新", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "更新失败", "error");
    } finally {
      actionLock.current = false;
      setSaving(false);
    }
  }

  async function removeGroup(group: AuthorizedGroup) {
    if (actionLock.current || !confirm(`确认删除授权群 ${group.chat_id}？`)) return;
    actionLock.current = true;
    setSaving(true);
    try {
      await apiFetch(`/api/admin/authorized-groups/${group.chat_id}`, {
        method: "DELETE",
      });
      await load();
      pushToast("已删除", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "删除失败", "error");
    } finally {
      actionLock.current = false;
      setSaving(false);
    }
  }

  return (
    <AdminShell title="群授权" subtitle="仅授权群可启用机器人">
      <Card>
        <CardHeader>
          <CardTitle>新增授权群</CardTitle>
        </CardHeader>
        <CardBody className="grid gap-3 sm:grid-cols-[1.2fr_1fr_auto]">
          <Input
            disabled={saving}
            placeholder="chat_id，例如 -1001234567890"
            value={chatId}
            onChange={(e) => setChatId(e.target.value)}
          />
          <Input
            disabled={saving}
            placeholder="群标题（可选）"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
          <Button onClick={createGroup} disabled={saving || hasDraft}>
            <Plus className="h-3.5 w-3.5" />
            添加
          </Button>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>已授权群 ({groups.length})</CardTitle>
        </CardHeader>
        {groups.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "暂无授权群"}
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeaderCell>Chat ID</TableHeaderCell>
                <TableHeaderCell>标题</TableHeaderCell>
                <TableHeaderCell>授权时间</TableHeaderCell>
                <TableHeaderCell>授权人</TableHeaderCell>
                <TableHeaderCell>启用</TableHeaderCell>
                <TableHeaderCell className="text-right">操作</TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {groups.map((group) => (
                <TableRow key={group.chat_id}>
                  <TableCell className="font-mono text-xs">{group.chat_id}</TableCell>
                  <TableCell>
                    <Input
                      disabled={saving}
                      value={group.title}
                      onChange={(e) => {
                        const next = e.target.value;
                        setGroups((items) =>
                          items.map((item) =>
                            item.chat_id === group.chat_id
                              ? { ...item, title: next }
                              : item,
                          ),
                        );
                      }}
                      onBlur={(e) => {
                        if (e.target.value !== savedTitles.current.get(group.chat_id)) {
                          void updateGroup(group, { title: e.target.value });
                        }
                      }}
                    />
                  </TableCell>
                  <TableCell className="text-[var(--text-muted)]">
                    {formatTime(group.authorized_at)}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {group.authorized_by ?? "系统"}
                  </TableCell>
                  <TableCell>
                    <Switch
                      disabled={saving || hasDraft}
                      checked={group.enabled}
                      onCheckedChange={(checked) =>
                        void updateGroup(group, { enabled: checked })
                      }
                    />
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={saving || hasDraft}
                      onClick={() => void removeGroup(group)}
                    >
                      <Trash2 className="h-3.5 w-3.5 text-[var(--danger)]" />
                    </Button>
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
