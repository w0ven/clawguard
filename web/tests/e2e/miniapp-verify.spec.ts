import { test, expect } from './ui-fixtures';

const token = '12345678-1234-1234-1234-123456789012';

test('Mini App identifies the user, tracks original deadline and submits both challenges', async ({ page }) => {
  const requests: unknown[] = [];
  await page.addInitScript(() => {
    const w = window as any;
    w.turnstile = { render: (_node: unknown, options: any) => { w.cf = options; return 'cf'; }, remove: () => {} };
    w.hcaptcha = { render: (_node: unknown, options: any) => { w.h = options; return 'h'; }, remove: () => {} };
  });
  await page.route('https://telegram.org/js/telegram-web-app.js', route => route.fulfill({ contentType: 'application/javascript', body: `window.Telegram={WebApp:{initData:'signed-fixture',ready(){},expand(){},safeAreaInset:{top:20,bottom:4},contentSafeAreaInset:{top:40,bottom:0}}}` }));
  await page.route('**/api/verify/mini/**', async route => {
    requests.push(route.request().postDataJSON());
    if (route.request().url().endsWith('/config')) return route.fulfill({ json: { turnstile_sitekey: 'cf', hcaptcha_sitekey: 'h', group_title: '测试群', user_id: 123, username: 'alice', expires_at: new Date(Date.now() + 120000).toISOString() } });
    return route.fulfill({ json: { success: true } });
  });
  await page.goto(`/verify/mini#${token}`);
  await expect(page.getByText('测试群')).toBeVisible();
  await expect(page.locator('main')).toHaveCSS('padding-top', '60px');
  await expect(page.getByText(/@alice.*123/)).toBeVisible();
  await expect(page.getByText(/剩余时间：0[12]:/)).toBeVisible();
  await expect(page.getByRole('button', { name: '提交验证' })).toBeDisabled();
  await expect.poll(() => page.evaluate(() => !!((window as any).cf && (window as any).h))).toBe(true);
  await page.evaluate(() => (window as any).cf.callback('cf-token'));
  await expect(page.getByRole('button', { name: '提交验证' })).toBeDisabled();
  await page.evaluate(() => (window as any).h.callback('h-token'));
  await page.getByRole('button', { name: '提交验证' }).click();
  await expect(page.getByText('本次入群验证限制已解除，其他未到期的限制不受影响。')).toBeVisible();
  expect(requests.slice(0, -1).length).toBeGreaterThan(0);
  expect(requests.slice(0, -1).every(value => JSON.stringify(value) === JSON.stringify({ token, init_data: 'signed-fixture' }))).toBe(true);
  expect(requests.at(-1)).toEqual({ token, init_data: 'signed-fixture', cf_response: 'cf-token', h_response: 'h-token' });
});

test('Mini App stops submission when the original deadline expires', async ({ page }) => {
  await page.addInitScript(() => {
    const w = window as any;
    w.turnstile = { render: (_node: unknown, opts: any) => { w.cf = opts; return 'cf'; }, remove: () => {} };
    w.hcaptcha = { render: (_node: unknown, opts: any) => { w.h = opts; return 'h'; }, remove: () => {} };
  });
  await page.route('https://telegram.org/js/telegram-web-app.js', route => route.fulfill({ contentType: 'application/javascript', body: `window.Telegram={WebApp:{initData:'signed',ready(){},expand(){}}}` }));
  await page.route('**/api/verify/mini/config', route => route.fulfill({ json: { turnstile_sitekey: 'cf', hcaptcha_sitekey: 'h', group_title: '测试群', user_id: 123, expires_at: new Date(Date.now() - 1000).toISOString() } }));
  await page.goto(`/verify/mini#${token}`);
  await expect(page.getByText('入群验证已超时，本次验证已关闭。')).toBeVisible();
  await expect.poll(() => page.evaluate(() => !!((window as any).cf && (window as any).h))).toBe(true);
  await page.evaluate(() => { (window as any).cf.callback('cf'); (window as any).h.callback('h'); });
  await expect(page.getByRole('button', { name: '提交验证' })).toBeDisabled();
});

test('Mini App without Telegram identity refuses verification', async ({ page }) => {
  await page.route('https://telegram.org/js/telegram-web-app.js', route => route.fulfill({ contentType: 'application/javascript', body: 'window.Telegram={WebApp:{initData:"",ready(){},expand(){}}}' }));
  await page.goto(`/verify/mini#${token}`);
  await expect(page.locator('main[role=alert]')).toContainText('请本人从机器人私聊打开');
});

test('Mini App frame policy is scoped to the verification page', async ({ request }) => {
  const mini = await request.get('/verify/mini');
  expect(mini.headers()['content-security-policy']).toBe("frame-ancestors 'self' https://web.telegram.org");
  expect(mini.headers()['x-frame-options']).toBeUndefined();
  const home = await request.get('/');
  expect(home.headers()['content-security-policy'] ?? '').not.toContain('web.telegram.org');
});
