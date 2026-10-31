import { test, expect, login, field } from './ui-fixtures';

const token = '12345678-1234-1234-1234-123456789012';

test('dual widgets require both, failure refreshes both, then success', async ({ page }) => {
  const submissions: unknown[] = [];
  let fail = true;
  await page.addInitScript(() => {
    const w = window as any;
    w.turnstile = { render: (_node: unknown, opts: any) => { w.cfOpts = opts; return 'cf'; }, remove: () => {} };
    w.hcaptcha = { ready: (fn: () => void) => fn(), render: (_node: unknown, opts: any) => { w.hOpts = opts; return 'h'; }, remove: () => {} };
  });
  await page.route('**/api/verify/dual/**', async route => {
    if (new URL(route.request().url()).pathname.endsWith('/config')) return route.fulfill({ json: { turnstile_sitekey: 'public-cf', hcaptcha_sitekey: 'public-h' } });
    submissions.push(route.request().postDataJSON());
    if (fail) return route.fulfill({ status: 409, json: { error: '验证失败，请刷新两项后重试' } });
    return route.fulfill({ json: { success: true } });
  });
  await page.goto(`/verify/dual#${token}`);
  await expect(page.getByRole('heading', { name: '双重人机验证' })).toBeVisible();
  await expect(page.getByRole('button', { name: '提交验证' })).toBeDisabled();
  await expect.poll(() => page.evaluate(() => Boolean((window as any).cfOpts && (window as any).hOpts))).toBe(true);
  expect(await page.evaluate(() => ({ action: (window as any).cfOpts.action, cdata: (window as any).cfOpts.cData }))).toEqual({ action: 'clawguard_dual', cdata: token });
  await page.evaluate(() => (window as any).cfOpts.callback('cf-one'));
  await expect(page.getByText('Cloudflare Turnstile：已完成')).toBeVisible();
  await expect(page.getByRole('button', { name: '提交验证' })).toBeDisabled();
  await page.evaluate(() => (window as any).hOpts.callback('h-one'));
  await page.getByRole('button', { name: '提交验证' }).click();
  await expect(page.locator('p[role=alert]')).toContainText('刷新两项');
  expect(submissions).toEqual([{ token, cf_response: 'cf-one', h_response: 'h-one' }]);
  fail = false;
  await page.getByRole('button', { name: '刷新两项重试' }).click();
  await expect(page.getByText('Cloudflare Turnstile：待完成')).toBeVisible();
  await expect(page.getByText('hCaptcha：待完成')).toBeVisible();
  await page.evaluate(() => { (window as any).cfOpts.callback('cf-two'); (window as any).hOpts.callback('h-two'); });
  await page.getByRole('button', { name: '提交验证' }).click();
  await expect(page.getByRole('heading', { name: '验证通过' })).toBeVisible();
  expect(submissions[1]).toEqual({ token, cf_response: 'cf-two', h_response: 'h-two' });
});

test('dual configuration is visible but reports missing keys in group and global', async ({ page }) => {
  await login(page);
  await page.goto('/groups/-1001');
  await page.getByRole('button', { name: '展开详细设置（4 项）' }).first().click();
  const groupField = field(page, '验证方式');
  await expect(groupField.getByText(/双重验证不可用/)).toBeVisible();
  await groupField.getByRole('checkbox').uncheck();
  await groupField.locator('select').selectOption('turnstile_hcaptcha');
  await expect(groupField.locator('select')).toHaveValue('turnstile_hcaptcha');
  page.once('dialog', d => d.accept());
  await page.goto('/groups/global-config');
  await expect(page.getByText(/双重验证不可用/)).toBeVisible();
  const globalField = page.getByText('验证方式', { exact: true }).locator('xpath=ancestor::div[contains(@class,"rounded")][1]');
  await expect(globalField.locator('select option[value="turnstile_hcaptcha"]')).toHaveCount(1);
});

test('legacy Turnstile link still submits only its own endpoint', async ({ page }) => {
  let submitted: unknown;
  await page.addInitScript(() => {
    const w = window as any;
    w.turnstile = { render: (_node: unknown, opts: any) => { w.legacyOpts = opts; return 'legacy'; }, remove: () => {} };
  });
  await page.route('**/api/verify/turnstile/**', async route => {
    submitted = route.request().postDataJSON();
    await route.fulfill({ json: { success: true } });
  });
  await page.goto(`/verify/${token}`);
  await expect.poll(() => page.evaluate(() => Boolean((window as any).legacyOpts))).toBe(true);
  await page.evaluate(() => (window as any).legacyOpts.callback('legacy-cf'));
  await expect(page.getByRole('heading', { name: '验证通过' })).toBeVisible();
  expect(submitted).toEqual({ cf_response: 'legacy-cf' });
});

test('hCaptcha script uses official explicit onload callback', async ({ page }) => {
  await page.addInitScript(() => {
    (window as any).turnstile = { render: (_node: unknown, opts: any) => { (window as any).cfOpts = opts; return 'cf'; }, remove: () => {} };
  });
  await page.route('https://js.hcaptcha.com/1/api.js?*', async route => {
    expect(new URL(route.request().url()).searchParams.get('onload')).toBe('__clawguardHcaptchaOnload');
    await route.fulfill({ contentType: 'application/javascript', body: `window.hcaptcha={render:(_el,opts)=>{window.hOpts=opts;return 'h'},remove:()=>{}};window.__clawguardHcaptchaOnload();` });
  });
  await page.route('**/api/verify/dual/config', route => route.fulfill({ json: { turnstile_sitekey: 'public-cf', hcaptcha_sitekey: 'public-h' } }));
  await page.goto(`/verify/dual#${token}`);
  await expect.poll(() => page.evaluate(() => Boolean((window as any).hOpts))).toBe(true);
  await expect(page.getByRole('button', { name: '提交验证' })).toBeDisabled();
});
