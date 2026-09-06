"use client";

import { useEffect, useState } from "react";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardHeader, CardTitle, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
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
import { Plus, X, Trash2 } from "lucide-react";

type FormState = {
  telegram_id: string;
  role: string;
  group_scope: string;
  notes: string;
};

const emptyForm: FormState = {
  telegram_id: "",
  role: "admin",
  group_scope: "",
  notes: "",
};

function parseGroupScope(value: string): number[] {
  return value
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map(Number)
    .filter((n) => Number.isFinite(n));
}

function formatTime(ts: string | null): string {
  if (!ts) return "从未";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "-";
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export default function AdminsPage() {
  const { pushToast } = useToast();
  const [admins, setAdmins] = useState<Admin[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [me, setMe] = useState<Admin | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [saving, setSaving] = useState(false);
  const [loading, setLoading] = useState(true);

  const isOwner = me?.role === "owner";

  async function load() {
    setLoading(true);
    try {
      const [a, g, m] = await Promise.all([
        apiFetch<{ admins: Admin[] }>("/api/admin/admins"),
        apiFetch<{ groups: Group[] }>("/api/admin/groups"),
        apiFetch<{ admin: Admin }>("/api/auth/me"),
      ]);
      setAdmins(a.admins ?? []);
      setGroups(g.groups ?? []);
      setMe(m.admin ?? null);
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  async function createAdmin() {
    if (!form.telegram_id) {
      pushToast("Telegram ID 不能为空", "error");
      return;
    }
    setSaving(true);
    try {
      await apiFetch("/api/admin/admins", {
        method: "POST",
        body: JSON.stringify({
          telegram_id: Number(form.telegram_id),
          role: form.role,
          group_scope: parseGroupScope(form.group_scope),
          notes: form.notes || null,
        }),
      });
      setForm(emptyForm);
      setShowCreate(false);
      await load();
      pushToast("已添加管理员", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "保存失败", "error");
    } finally {
      setSaving(false);
    }
  }

  async function updateAdmin(
    admin: Admin,
    patch: Partial<Pick<Admin, "role" | "notes" | "group_scope">>,
  ) {
    try {
      await apiFetch(`/api/admin/admins/${admin.id}`, {
        method: "PUT",
        body: JSON.stringify({
          role: patch.role ?? admin.role,
          notes: patch.notes !== undefined ? patch.notes : admin.notes,
          group_scope: patch.group_scope ?? admin.group_scope ?? [],
        }),
      });
      await load();
      pushToast("已更新", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "更新失败", "error");
    }
  }

  async function removeAdmin(admin: Admin) {
    if (!confirm(`确认删除管理员 ${admin.telegram_id}？`)) return;
    try {
      await apiFetch(`/api/admin/admins/${admin.id}`, { method: "DELETE" });
      await load();
      pushToast("已删除", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "删除失败", "error");
    }
  }

  return (
    <AdminShell
      title="管理员"
      subtitle="面板登录授权与权限分组"
      actions={
        isOwner ? (
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="h-3.5 w-3.5" />
            新增管理员
          </Button>
        ) : null
      }
    >
      {/* Create Modal */}
      {isOwner && showCreate ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-sm px-4"
          onClick={(e) => {
            if (e.target === e.currentTarget) setShowCreate(false);
          }}
        >
          <Card className="w-full max-w-lg shadow-xl">
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle>新增管理员</CardTitle>
              <button
                onClick={() => setShowCreate(false)}
                className="text-[var(--text-muted)] hover:text-[var(--text)]"
              >
                <X className="h-4 w-4" />
              </button>
            </CardHeader>
            <CardBody className="space-y-4">
              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <label className="block text-xs font-medium text-[var(--text-muted)] mb-1.5">
                    Telegram ID
                  </label>
                  <Input
                    placeholder="例如 123456789"
                    value={form.telegram_id}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, telegram_id: e.target.value }))
                    }
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-[var(--text-muted)] mb-1.5">
                    角色
                  </label>
                  <Select
                    value={form.role}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, role: e.target.value }))
                    }
                  >
                    <option value="admin">Admin</option>
                    <option value="owner">Owner</option>
                  </Select>
                </div>
              </div>
              <div>
                <label className="block text-xs font-medium text-[var(--text-muted)] mb-1.5">
                  可管理群 (group IDs, 空格/逗号分隔，留空 = 全部)
                </label>
                <Textarea
                  placeholder="-1001234, -1005678"
                  value={form.group_scope}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, group_scope: e.target.value }))
                  }
                  className="font-mono text-xs"
                />
                {groups.length > 0 && (
                  <p className="mt-1.5 text-xs text-[var(--text-muted)]">
                    可用群：
                    {groups.map((g) => `${g.chat_id} (${g.title})`).join("、")}
                  </p>
                )}
              </div>
              <div>
                <label className="block text-xs font-medium text-[var(--text-muted)] mb-1.5">
                  备注（可选）
                </label>
                <Textarea
                  value={form.notes}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, notes: e.target.value }))
                  }
                />
              </div>
              <div className="flex justify-end gap-2">
                <Button
                  variant="secondary"
                  onClick={() => setShowCreate(false)}
                  disabled={saving}
                >
                  取消
                </Button>
                <Button onClick={createAdmin} disabled={saving}>
                  {saving ? "保存中…" : "保存"}
                </Button>
              </div>
            </CardBody>
          </Card>
        </div>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>管理员列表 ({admins.length})</CardTitle>
        </CardHeader>
        {admins.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "无管理员"}
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeaderCell>Telegram ID</TableHeaderCell>
                <TableHeaderCell>用户</TableHeaderCell>
                <TableHeaderCell>角色</TableHeaderCell>
                <TableHeaderCell>管理范围</TableHeaderCell>
                <TableHeaderCell>最后登录</TableHeaderCell>
                <TableHeaderCell>备注</TableHeaderCell>
                <TableHeaderCell></TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {admins.map((admin) => (
                <TableRow key={admin.id}>
                  <TableCell className="tabular-nums">{admin.telegram_id}</TableCell>
                  <TableCell>
                    {admin.username
                      ? `@${admin.username}`
                      : admin.first_name || "—"}
                  </TableCell>
                  <TableCell>
                    {isOwner ? (
                      <Select
                        value={admin.role}
                        onChange={(e) =>
                          updateAdmin(admin, { role: e.target.value })
                        }
                        className="w-28"
                      >
                        <option value="admin">Admin</option>
                        <option value="owner">Owner</option>
                      </Select>
                    ) : (
                      <Badge tone={admin.role === "owner" ? "info" : "default"}>
                        {admin.role}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>
                    {(admin.group_scope?.length ?? 0) === 0 ? (
                      <Badge tone="info">全部</Badge>
                    ) : (
                      <span className="text-[var(--text-muted)]">
                        {admin.group_scope.length} 个群
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="text-[var(--text-muted)] tabular-nums whitespace-nowrap">
                    {formatTime(admin.last_login_at)}
                  </TableCell>
                  <TableCell className="text-[var(--text-muted)] max-w-[200px] truncate">
                    {admin.notes || "—"}
                  </TableCell>
                  <TableCell>
                    {isOwner && me?.id !== admin.id ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => removeAdmin(admin)}
                      >
                        <Trash2 className="h-3.5 w-3.5 text-[var(--danger)]" />
                      </Button>
                    ) : null}
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
