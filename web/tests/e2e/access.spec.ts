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
