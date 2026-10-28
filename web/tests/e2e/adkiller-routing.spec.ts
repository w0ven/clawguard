import { test, expect, login } from './ui-fixtures';

test('AdKiller routing explanation and existing settings', async ({ page, mock }) => {
  await login(page);
  await page.goto('/llm');
  await page.getByRole('button', { name: 'AdKiller', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'AdKiller 前置广告检测（仅图片）与文字兜底' })).toBeVisible();
  await expect(page.getByText(/文字和简介先走完整模型链/)).toBeVisible();
  await expect(page.getByText(/图片先用 AdKiller 检测图片中的文字/)).toBeVisible();
  await expect(page.getByText(/图片 OCR 不能代替画面审核/)).toBeVisible();
  await expect(page.getByText(/旧版“跳过后续模型”设置保留但不再生效/)).toBeVisible();
  const timeout = page.getByRole('spinbutton', { name: /文字超时/ });
  await timeout.fill('2000');
  await page.getByRole('button', { name: '保存 AdKiller 设置', exact: true }).click();
  await expect.poll(() => mock.writes.length).toBe(1);
  expect(mock.writes[0].body.config.ai.adkiller.timeout_ms).toBe(2000);
  expect(mock.writes[0].body.config.ai.adkiller.on_failure).toBe('fallback');
});
