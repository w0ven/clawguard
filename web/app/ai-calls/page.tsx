"use client";

import { useEffect, useState } from "react";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
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
import type { AICacheStats, AICallSummary } from "@/lib/types";
import { useToast } from "@/components/providers";

function sceneBadge(scene: string | undefined): { text: string; className: string } {
  if (scene === "bio") return { text: "简介审核", className: "border-purple-500/20 bg-purple-500/10 text-purple-700" };
  return { text: "消息审核", className: "border-blue-500/20 bg-blue-500/10 text-blue-700" };
}

function CallTrendChart({
  points,
}: {
  points: { date: string; calls: number }[];
}) {
  const width = 760;
  const height = 220;
  const padding = 24;
  const max = Math.max(...points.map((item) => item.calls), 1);
  const stepX =
    points.length > 1 ? (width - padding * 2) / (points.length - 1) : 0;
  const path = points
    .map((point, index) => {
      const x = padding + stepX * index;
      const y = height - padding - (point.calls / max) * (height - padding * 2);
      return `${index === 0 ? "M" : "L"} ${x} ${y}`;
    })
    .join(" ");

  return (
    <div className="space-y-3">
      <div className="overflow-x-auto">
        <svg
          viewBox={`0 0 ${width} ${height}`}
          className="min-w-[640px] text-[var(--accent)]"
          role="img"
          aria-label="最近 30 天 AI 调用次数趋势图"
        >
          <defs>
            <linearGradient id="callTrendFill" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stopColor="#2563eb" stopOpacity="0.24" />
              <stop offset="100%" stopColor="#2563eb" stopOpacity="0" />
            </linearGradient>
          </defs>
          <line
            x1={padding}
            y1={height - padding}
            x2={width - padding}
            y2={height - padding}
            stroke="currentColor"
            strokeOpacity="0.18"
          />
          <line
            x1={padding}
            y1={padding}
            x2={padding}
            y2={height - padding}
            stroke="currentColor"
            strokeOpacity="0.18"
          />
          <path
            d={`${path} L ${width - padding} ${height - padding} L ${padding} ${height - padding} Z`}
            fill="url(#callTrendFill)"
            stroke="none"
          />
          <path
            d={path}
            fill="none"
            stroke="currentColor"
            strokeWidth="3"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
          {points.map((point, index) => {
            const x = padding + stepX * index;
            const y =
              height - padding - (point.calls / max) * (height - padding * 2);
            return (
              <circle
                key={point.date}
                cx={x}
                cy={y}
                r="3.5"
                fill="#2563eb"
                stroke="var(--surface)"
                strokeWidth="2"
              />
            );
          })}
        </svg>
      </div>
      <div className="grid grid-cols-3 gap-2 text-xs text-[var(--text-muted)] sm:grid-cols-6">
        {points
          .filter((_, index) => index % 5 === 0 || index === points.length - 1)
          .map((point) => (
            <div
              key={point.date}
              className="rounded-md border border-[var(--border)] bg-[var(--surface-2)] px-2 py-2"
            >
              <div>{point.date.slice(5)}</div>
              <div className="mt-1 font-medium text-[var(--text)]">
                {point.calls}
              </div>
            </div>
          ))}
      </div>
    </div>
  );
}

export default function AICallsPage() {
  const { pushToast } = useToast();
  const [summary, setSummary] = useState<AICallSummary | null>(null);
  const [cacheStats, setCacheStats] = useState<AICacheStats | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    Promise.all([
      apiFetch<AICallSummary>("/api/admin/ai-calls"),
      apiFetch<AICacheStats>("/api/admin/ai-cache-stats"),
    ])
      .then(([callsPayload, cachePayload]) => {
        setSummary(callsPayload);
        setCacheStats(cachePayload);
      })
      .catch((e) =>
        pushToast(e instanceof Error ? e.message : "加载失败", "error"),
      )
      .finally(() => setLoading(false));
  }, [pushToast]);

  const modelTotal = (summary?.per_model ?? []).reduce(
    (acc, item) => acc + item.calls,
    0,
  );
  const cacheTotal = (cacheStats?.hit ?? 0) + (cacheStats?.miss ?? 0);

  return (
    <AdminShell
      title="AI 调用统计"
      subtitle="调用次数、缓存命中率与模型/群分布"
    >
      <section className="grid gap-3 grid-cols-1 sm:grid-cols-2 xl:grid-cols-4">
        <Card className="p-4">
          <p className="text-xs text-[var(--text-muted)]">今日调用次数</p>
          <p className="mt-2 text-3xl font-semibold tabular-nums">
            {summary?.today_calls ?? 0}
          </p>
        </Card>
        <Card className="p-4">
          <p className="text-xs text-[var(--text-muted)]">近 30 天调用</p>
          <p className="mt-2 text-3xl font-semibold tabular-nums">
            {modelTotal}
          </p>
        </Card>
        <Card className="p-4">
          <p className="text-xs text-[var(--text-muted)]">有调用模型</p>
          <p className="mt-2 text-3xl font-semibold tabular-nums">
            {summary?.per_model.length ?? 0}
          </p>
        </Card>
        <Card className="p-4">
          <p className="text-xs text-[var(--text-muted)]">缓存命中率</p>
          <p className="mt-2 text-3xl font-semibold tabular-nums">
            {((cacheStats?.rate ?? 0) * 100).toFixed(1)}%
          </p>
          <p className="mt-2 text-xs text-[var(--text-muted)]">
            命中 {cacheStats?.hit ?? 0} / 总计 {cacheTotal}
          </p>
        </Card>
      </section>

      <Card>
        <CardHeader>
          <CardTitle>最近 30 天每日调用次数</CardTitle>
        </CardHeader>
        <CardBody>
          {!summary || summary.daily.length === 0 ? (
            <div className="py-12 text-center text-sm text-[var(--text-muted)]">
              {loading ? "加载中…" : "暂无 AI 调用记录"}
            </div>
          ) : (
            <CallTrendChart points={summary.daily} />
          )}
        </CardBody>
      </Card>

      <div className="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>按审核类型</CardTitle>
          </CardHeader>
          {(summary?.per_scene.length ?? 0) === 0 ? (
            <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
              {loading ? "加载中…" : "暂无 AI 调用记录"}
            </div>
          ) : (
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeaderCell>审核类型</TableHeaderCell>
                  <TableHeaderCell>调用次数</TableHeaderCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {summary?.per_scene.map((item) => {
                  const scene = sceneBadge(item.scene);
                  return (
                    <TableRow key={item.scene}>
                      <TableCell>
                        <Badge className={scene.className}>{scene.text}</Badge>
                      </TableCell>
                      <TableCell className="tabular-nums">{item.calls}</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>按模型</CardTitle>
          </CardHeader>
          {(summary?.per_model.length ?? 0) === 0 ? (
            <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
              {loading ? "加载中…" : "暂无 AI 调用记录"}
            </div>
          ) : (
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeaderCell>模型</TableHeaderCell>
                  <TableHeaderCell>调用次数</TableHeaderCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {summary?.per_model.map((item) => (
                  <TableRow key={item.model}>
                    <TableCell>
                      <code className="text-xs">{item.model}</code>
                    </TableCell>
                    <TableCell className="tabular-nums">{item.calls}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>按群 Top 10</CardTitle>
          </CardHeader>
          {(summary?.per_chat.length ?? 0) === 0 ? (
            <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
              {loading ? "加载中…" : "暂无 AI 调用记录"}
            </div>
          ) : (
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeaderCell>群</TableHeaderCell>
                  <TableHeaderCell>Chat ID</TableHeaderCell>
                  <TableHeaderCell>调用次数</TableHeaderCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {summary?.per_chat.map((item) => (
                  <TableRow key={item.chat_id}>
                    <TableCell>{item.title || "未命名群"}</TableCell>
                    <TableCell className="tabular-nums">
                      {item.chat_id}
                    </TableCell>
                    <TableCell className="tabular-nums">{item.calls}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card>
      </div>
    </AdminShell>
  );
}
