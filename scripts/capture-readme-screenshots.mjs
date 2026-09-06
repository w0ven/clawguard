import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "../web/node_modules/playwright/index.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const outDir = path.join(root, "docs/screenshots");
const baseURL = "http://127.0.0.1:3100";

const now = "2026-09-06T12:00:00Z";

function json(route, body, status = 200) {
  return route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  });
}

async function mockAdminApis(page) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;

    if (p === "/api/auth/me") {
      return json(route, { admin: { id: 1, role: "super", telegram_id: 123456789 } });
    }
    if (p === "/api/admin/stats") {
      return json(route, {
        groups_count: 3,
        active_verifications: 2,
        today_violations: 4,
      });
    }
    if (p === "/api/admin/system-state") {
      return json(route, {
        state: {
          id: 1,
          ai_paused: false,
          actions_paused: false,
          frozen: false,
          ai_paused_reason: "",
          updated_at: now,
          updated_by: 123456789,
        },
      });
    }
    if (p.startsWith("/api/admin/violations")) {
      return json(route, {
        violations: [
          {
            id: 11,
            chat_id: -1001234567890,
            user_id: 10001,
            username: "spammer",
            rule: "keyword",
            matched: "广告",
            action: "delete",
            message_text: "示例广告",
            created_at: now,
          },
          {
            id: 12,
            chat_id: -1001234567890,
            user_id: 10002,
            username: null,
            rule: "cas",
            matched: null,
            action: "ban",
            message_text: null,
            created_at: now,
          },
        ],
      });
    }
    if (p === "/api/admin/health") {
      return json(route, {
        db_latency_ms: 3,
        redis_latency_ms: 1,
        webhook_last_update_at: now,
        webhook_seconds_ago: 8,
        ai_last_ok_at: now,
        ai_last_fail_at: null,
        ai_last_error: "",
        today_calls: 128,
        uptime_seconds: 86400,
        active_pending: 2,
        due_cleanup: 0,
        retrying_cleanup: 0,
        oldest_due_seconds: 0,
        join_cleanup_dead: 0,
        backup_last_at: now,
        backup_seconds_ago: 3600,
        verification_worker_seconds_ago: 12,
        join_recovery_worker_seconds_ago: 15,
      });
    }
    if (p.startsWith("/api/admin/events")) {
      return json(route, {
        events: [
          {
            type: "violation",
            id: 11,
            chat_id: -1001234567890,
            user_id: 10001,
            title: "关键词命中",
            detail: "删除广告消息",
            extra: "keyword",
            created_at: now,
          },
          {
            type: "ai_decision",
            id: 21,
            chat_id: -1001234567890,
            user_id: 10003,
            title: "AI 判定通过",
            detail: "普通讨论",
            extra: "allow",
            created_at: now,
          },
        ],
        total: 2,
        has_more: false,
      });
    }
    return json(route, {});
  });
}

const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  colorScheme: "light",
  locale: "zh-CN",
  deviceScaleFactor: 2,
});
const page = await context.newPage();

await mkdir(outDir, { recursive: true });

await page.goto(`${baseURL}/`, { waitUntil: "networkidle" });
await page.getByRole("heading", { name: "ClawGuard" }).waitFor();
await page.screenshot({
  path: path.join(outDir, "login.png"),
  fullPage: true,
});

await mockAdminApis(page);
await context.addCookies([
  { name: "cg_admin", value: "screenshot", url: baseURL },
]);
await page.goto(`${baseURL}/dashboard`, { waitUntil: "networkidle" });
await page.getByText("实时群管理状态").waitFor();
await page.getByText("管理中的群").waitFor();
await page.locator("text=3").first().waitFor();
await page.screenshot({
  path: path.join(outDir, "dashboard.png"),
  fullPage: true,
});

await browser.close();
console.log(`wrote ${outDir}/login.png and dashboard.png`);
