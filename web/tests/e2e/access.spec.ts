// Original access assertions unchanged; isolate network using test-only fixtures.
import { expect, test } from "./ui-fixtures";

test("login page renders without horizontal overflow", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("h1")).toHaveText("ClawGuard");
  await expect(page.locator("body")).toBeVisible();

  const overflows = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  expect(overflows).toBe(false);
});

test("protected dashboard redirects unauthenticated visitors", async ({ page }) => {
  await page.goto("/dashboard");
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator("h1")).toHaveText("ClawGuard");
});

test("mini app entry explains that Telegram is required", async ({ page }) => {
  await page.goto("/miniapp");
  await expect(page.locator("h1")).toHaveText("ClawGuard");
  await expect(page.getByText("请从 Telegram Bot 内打开此管理面板")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("mini app shell exposes every management destination", async ({ page, context }) => {
  await context.addCookies([{ name: "cg_admin", value: "test", domain: "127.0.0.1", path: "/" }]);
  await page.addInitScript(() => {
    sessionStorage.setItem("cg_miniapp", "1");
    window.Telegram = {
      WebApp: {
        initData: "test-init-data",
        colorScheme: "dark",
        ready() {},
        expand() {},
        close() {},
        BackButton: { show() {}, hide() {}, onClick() {}, offClick() {} },
      },
    };
  });
  await page.goto("/dashboard");
  await expect(page.locator("html")).toHaveClass(/telegram-miniapp/);
  const mobile = (page.viewportSize()?.width ?? 0) < 768;
  if (mobile) {
    await expect(page.getByText("Mini App")).toBeVisible();
    await page.getByRole("button", { name: "全部" }).click();
    await expect(page.getByRole("heading", { name: "全部功能" })).toBeVisible();
  }

  for (const label of ["总览", "群管理", "群授权", "违规", "AI 复核", "AI 统计", "信任系统", "Prompt", "模型管理", "审计", "管理员"]) {
    await expect(page.getByRole("link", { name: label, exact: true }).last()).toBeVisible();
  }
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("web challenge polling survives countdown ticks and redirects to dashboard upon approval", async ({ page, context }) => {
  let pollCalls = 0;

  await page.route("**/api/auth/web-challenge", async (route) => {
    await route.fulfill({
      status: 200,
      json: {
        challenge_id: "0123456789abcdef0123456789abcdef",
        code: "86Z9MS",
        bot_username: "rfcguard_bot",
        tg_deep_link: "https://t.me/rfcguard_bot?start=weblogin_86Z9MS",
        expires_in: 300,
      },
    });
  });

  await page.route("**/api/auth/web-poll", async (route) => {
    pollCalls += 1;
    if (pollCalls === 1) {
      await route.fulfill({ status: 200, json: { status: "pending" } });
      return;
    }
    // Hold the second poll across a 1-second countdown tick to ensure countdown re-renders never cancel it.
    await new Promise((resolve) => setTimeout(resolve, 1250));
    await context.addCookies([{ name: "cg_admin", value: "fixture-approved", domain: "127.0.0.1", path: "/" }]);
    await route.fulfill({
      status: 200,
      json: { status: "approved", redirect_to: "/dashboard" },
    });
  });

  await page.goto("/");
  await page.getByRole("button", { name: "通过 Telegram 客户端验证登录" }).click();
  await expect(page.getByText("86Z9MS")).toBeVisible();

  await expect(page).toHaveURL(/\/dashboard$/, { timeout: 10000 });
  expect(pollCalls).toBeGreaterThanOrEqual(2);
});

