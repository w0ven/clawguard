"use client";

import { useCallback, useEffect, useState } from "react";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { apiFetch } from "@/lib/api";

type Entry = {
  user_id: string;
  note: string;
  expires_at: string | null;
  created_at: string;
};


export function GroupExemptUsers({ chatId }: { chatId: string }) {
  const endpoint = `/api/admin/groups/${chatId}/exempt-users`;
  const [users, setUsers] = useState<Entry[]>([]);
  const [id, setId] = useState("");
  const [note, setNote] = useState("");
  const [expires, setExpires] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await apiFetch<{ users: Entry[] }>(endpoint);
      setUsers(data.users);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "加载失败");
    } finally {
      setLoading(false);
    }
  }, [endpoint]);

  useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (!/^[0-9]+$/.test(id) || /^0+$/.test(id)) {
      setError("请输入正整数 Telegram 用户 ID");
      return;
    }
    const until = expires ? new Date(expires) : null;
    if (until && (isNaN(until.getTime()) || until.getTime() <= Date.now())) {
      setError("到期时间必须在未来");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await apiFetch(`${endpoint}/${id}`, {
        method: "PUT",
        body: JSON.stringify({
          note: note.trim(),
          expires_at: until?.toISOString() ?? null,
        }),
      });
      setId("");
      setNote("");
      setExpires("");
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "保存失败");
    } finally {
      setBusy(false);
    }
  }

  async function revoke(user: Entry) {
    if (!confirm(`确认撤销用户 ${user.user_id} 的本群豁免？`)) return;
    setBusy(true);
    setError("");
    try {
      await apiFetch(`${endpoint}/${user.user_id}`, { method: "DELETE" });
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "撤销失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-label="本群用户豁免" className="grid gap-4">
      <h2 className="text-xl font-semibold">本群用户豁免</h2>
      <Card>
        <CardHeader><CardTitle>添加或更新豁免</CardTitle></CardHeader>
        <CardBody className="grid gap-3">
          <p className="text-sm text-[var(--text-muted)]">
            跳过自动审核和入群验证，不影响关键词自动回复、手动处罚或已有封禁。
            不会自动解除已有禁言或封禁。已有待验证任务在豁免期间暂停处罚，限制仍可能存在，
            需管理员人工检查；撤销或到期后待处理任务可能继续执行。
          </p>
          <label className="grid gap-1 text-sm">
            Telegram 用户 ID
            <Input
              aria-label="Telegram 用户 ID"
              inputMode="numeric"
              value={id}
              onChange={(e) => setId(e.target.value)}
              placeholder="正整数 ID"
            />
          </label>
          <p className="text-sm text-[var(--text-muted)]">仅限真人成员；已知机器人请使用独立机器人白名单。尚未识别的 ID 可预先添加，但识别为机器人后不会生效。</p>
          <label className="grid gap-1 text-sm">
            备注（可选）
            <Input
              aria-label="备注"
              value={note}
              maxLength={500}
              onChange={(e) => setNote(e.target.value)}
            />
          </label>
          <label className="grid gap-1 text-sm">
            到期时间（留空永久）
            <Input
              aria-label="到期时间"
              type="datetime-local"
              value={expires}
              onChange={(e) => setExpires(e.target.value)}
            />
          </label>
          <Button type="button" disabled={busy} onClick={save}>保存豁免</Button>
        </CardBody>
      </Card>
      {error && <p role="alert" className="text-sm text-[var(--danger)]">{error}</p>}
      <Card>
        <CardHeader><CardTitle>豁免名单</CardTitle></CardHeader>
        <CardBody className="grid gap-3">
          {loading ? (
            <p>加载中…</p>
          ) : users.length === 0 ? (
            <p>暂无豁免用户</p>
          ) : users.map((user) => (
            <div key={user.user_id} className="flex flex-wrap items-center justify-between gap-3 border-b py-2">
              <div>
                <strong>{user.user_id}</strong>
                <p className="text-sm text-[var(--text-muted)]">
                  {user.note || "无备注"} · {user.expires_at
                    ? `${new Date(user.expires_at).toLocaleString()} ${new Date(user.expires_at).getTime() <= Date.now() ? "（已过期）" : "到期"}`
                    : "永久"}
                </p>
              </div>
              <Button
                type="button"
                variant="secondary"
                disabled={busy}
                onClick={() => revoke(user)}
                aria-label={`撤销 ${user.user_id}`}
              >
                撤销
              </Button>
            </div>
          ))}
        </CardBody>
      </Card>
    </section>
  );
}
