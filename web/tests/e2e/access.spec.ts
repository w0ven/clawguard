import { expect, test } from "@playwright/test";

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
