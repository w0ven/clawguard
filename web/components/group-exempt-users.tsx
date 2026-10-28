"use client";

import { useCallback, useEffect, useRef, useState } from "react";
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
const PAGE_SIZE = 20;

export function GroupExemptUsers({ chatId }: { chatId: string }) {
  const endpoint = `/api/admin/groups/${chatId}/exempt-users`;
  const [users, setUsers] = useState<Entry[]>([]);
  const [id, setId] = useState("");
  const [note, setNote] = useState("");
  const [expires, setExpires] = useState("");
  const [editing, setEditing] = useState(false);
  const [originalExpiry, setOriginalExpiry] = useState<string | null>(null);
  const [expiryChanged, setExpiryChanged] = useState(false);
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("all");
  const [page, setPage] = useState(0);
  const [total, setTotal] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const currentChat = useRef(chatId);
  currentChat.current = chatId;
  const requestKey = `${chatId}|${page}|${status}|${search}`;
  const currentRequest = useRef(requestKey);
  currentRequest.current = requestKey;

  const clearEdit = useCallback(() => {
    setId("");
    setNote("");
    setExpires("");
    setEditing(false);
    setOriginalExpiry(null);
    setExpiryChanged(false);
  }, []);

  useEffect(() => {
    generation.current++;
    clearEdit();
    setUsers([]);
    setTotal(0);
    setPage(0);
    setSearch("");
    setStatus("all");
    setError("");
  }, [chatId, clearEdit]);

  const load = useCallback(async () => {
    if (requestKey !== currentRequest.current) return;
    const sequence = ++generation.current;
    setLoading(true);
    const params = new URLSearchParams({ search, status, limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
    try {
      const data = await apiFetch<{ users: Entry[]; total?: number; has_more?: boolean }>(`${endpoint}?${params}`);
      if (sequence !== generation.current || requestKey !== currentRequest.current) return;
      if (page > 0 && data.users.length === 0) { setPage(page - 1); return; }
      setUsers(data.users);
      setTotal(data.total ?? data.users.length);
      setHasMore(data.has_more ?? false);
      setError("");
    } catch (e) {
      if (sequence === generation.current && requestKey === currentRequest.current) setError(e instanceof Error ? e.message : "加载失败");
    } finally {
      if (sequence === generation.current && requestKey === currentRequest.current) setLoading(false);
    }
  }, [endpoint, page, search, status, requestKey]);

  useEffect(() => {
    void load();
    return () => { generation.current++; };
  }, [load]);

  function edit(user: Entry) {
    setEditing(true);
    setId(user.user_id);
    setNote(user.note);
    setOriginalExpiry(user.expires_at);
    setExpiryChanged(false);
    if (user.expires_at) {
      const d = new Date(user.expires_at);
      const pad = (n: number) => String(n).padStart(2, "0");
      setExpires(`${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`);
    } else setExpires("");
  }

  async function save() {
    if (!/^[0-9]+$/.test(id) || /^0+$/.test(id)) { setError("请输入正整数 Telegram 用户 ID"); return; }
    const until = editing && !expiryChanged && originalExpiry ? new Date(originalExpiry) : expires ? new Date(expires) : null;
    if (until && (isNaN(until.getTime()) || (until.getTime() <= Date.now() && !(editing && !expiryChanged && originalExpiry)))) { setError("到期时间必须在未来"); return; }
    const targetChat = chatId;
    setBusy(true);
    setError("");
    try {
      await apiFetch(`${endpoint}/${id}`, {
        method: "PUT",
        body: JSON.stringify({
          note: note.trim(),
          expires_at: editing && !expiryChanged ? originalExpiry : until?.toISOString() ?? null,
        }),
      });
      if (currentChat.current === targetChat) { clearEdit(); await load(); }
    } catch (e) {
      if (currentChat.current === targetChat) setError(e instanceof Error ? e.message : "保存失败");
    } finally { setBusy(false); }
  }

  async function revoke(user: Entry) {
    if (!confirm(`确认撤销用户 ${user.user_id} 的本群豁免？`)) return;
    const targetChat = chatId;
    setBusy(true);
    setError("");
    try {
      await apiFetch(`${endpoint}/${user.user_id}`, { method: "DELETE" });
      if (currentChat.current === targetChat) { if (editing && id === user.user_id) clearEdit(); await load(); }
    } catch (e) {
      if (currentChat.current === targetChat) setError(e instanceof Error ? e.message : "撤销失败");
    } finally { setBusy(false); }
  }

  return (
    <section aria-label="本群用户豁免" className="grid gap-4">
      <h2 className="text-xl font-semibold">本群用户豁免</h2>
      <Card>
        <CardHeader>
          <CardTitle>添加或更新豁免</CardTitle>
        </CardHeader>
        <CardBody className="grid gap-3">
          <p className="text-sm text-[var(--text-muted)]">
            跳过自动审核和入群验证，不影响关键词自动回复、手动处罚或已有封禁。不会自动解除已有禁言或封禁。已有待验证任务在豁免期间暂停处罚，限制仍可能存在，需管理员人工检查；撤销或到期后待处理任务可能继续执行。
          </p>
          <label className="grid gap-1 text-sm">
            Telegram 用户 ID
            <Input
              aria-label="Telegram 用户 ID"
              inputMode="numeric"
              value={id}
              disabled={editing}
              onChange={(e) => setId(e.target.value)}
              placeholder="正整数 ID"
            />
          </label>
          <p className="text-sm text-[var(--text-muted)]">
            仅限真人成员；已知机器人请使用独立机器人白名单。尚未识别的 ID 可预先添加，但识别为机器人后不会生效。
          </p>
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
              step="1"
              value={expires}
              onChange={(e) => {
                setExpires(e.target.value);
                setExpiryChanged(true);
              }}
            />
          </label>
          <Button type="button" disabled={busy} onClick={save}>
            保存豁免
          </Button>
          {editing && (
            <Button type="button" variant="secondary" disabled={busy} onClick={clearEdit}>
              取消编辑
            </Button>
          )}
        </CardBody>
      </Card>
      {error && <p role="alert" className="text-sm text-[var(--danger)]">{error}</p>}
      <Card>
        <CardHeader>
          <CardTitle>豁免名单</CardTitle>
        </CardHeader>
        <CardBody className="grid gap-3">
          <label>
            搜索用户 ID 或备注
            <Input
              aria-label="搜索豁免"
              value={search}
              onChange={(e) => {
                generation.current++;
                setPage(0);
                setSearch(e.target.value);
              }}
            />
          </label>
          <label>
            状态
            <select
              aria-label="豁免状态"
              value={status}
              onChange={(e) => {
                generation.current++;
                setPage(0);
                setStatus(e.target.value);
              }}
            >
              <option value="all">全部</option>
              <option value="active">有效</option>
              <option value="expired">已过期</option>
            </select>
          </label>
          <p>共 {total} 条 · 第 {page + 1} 页</p>
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
              <div className="flex gap-2">
                <Button
                  type="button"
                  variant="secondary"
                  disabled={busy}
                  onClick={() => edit(user)}
                  aria-label={`编辑 ${user.user_id}`}
                >
                  编辑
                </Button>
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
            </div>
          ))}
          <div className="flex gap-2">
            <Button
              type="button"
              variant="secondary"
              disabled={loading || page === 0}
              onClick={() => {
                generation.current++;
                setPage(page - 1);
              }}
            >
              上一页
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={loading || !hasMore}
              onClick={() => {
                generation.current++;
                setPage(page + 1);
              }}
            >
              下一页
            </Button>
          </div>
        </CardBody>
      </Card>
    </section>
  );
}
