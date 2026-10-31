import { test, expect } from './ui-fixtures';

// Equivalent to the SDK URL hash parser for the Telegram launch shapes below.
const telegramSDKFixture = `(() => {
 const hash = location.hash.slice(1); const parts = hash.split('?');
 const params = new URLSearchParams(parts.length > 1 ? parts.slice(1).join('?') : parts[0]);
 window.Telegram = { WebApp: { initData: params.get('tgWebAppData') || '', ready(){}, expand(){}, BackButton:{onClick(){},offClick(){},show(){},hide(){}}, safeAreaInset:{top:20,bottom:4},contentSafeAreaInset:{top:40,bottom:0} } };
})();`;
const telegramFragment = (value: string, prefix = '') => `#${prefix}tgWebAppData=${encodeURIComponent(value)}&tgWebAppVersion=8.0&tgWebAppPlatform=web`;

const token = '12345678-1234-1234-1234-123456789012';

test('Mini App identifies the user, tracks original deadline and submits both challenges', async ({ page }) => {
  const requests: unknown[] = [];
  await page.addInitScript(() => {
    const w = window as any;
    w.turnstile = { render: (_node: unknown, options: any) => { w.cf = options; return 'cf'; }, remove: () => {} };
    w.hcaptcha = { render: (_node: unknown, options: any) => { w.h = options; return 'h'; }, remove: () => {} };
  });
  await page.route('https://telegram.org/js/telegram-web-app.js*', route => route.fulfill({ contentType: 'application/javascript', body: telegramSDKFixture }));
  await page.route('**/api/verify/mini/**', async route => {
    requests.push(route.request().postDataJSON());
    if (route.request().url().endsWith('/config')) return route.fulfill({ json: { turnstile_sitekey: 'cf', hcaptcha_sitekey: 'h', group_title: '测试群', user_id: 123, username: 'alice', remaining_ms: 120000 } });
    return route.fulfill({ json: { success: true } });
  });
  await page.goto(`/verify/mini?task=${token}${telegramFragment('signed-fixture', 'unused?')}`);
  await expect.poll(() => page.evaluate(() => location.search)).toBe('');
  await expect.poll(() => page.evaluate(() => location.hash)).toContain('tgWebAppData');
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
  await page.route('https://telegram.org/js/telegram-web-app.js*', route => route.fulfill({ contentType: 'application/javascript', body: telegramSDKFixture }));
  await page.route('**/api/verify/mini/config', route => route.fulfill({ json: { turnstile_sitekey: 'cf', hcaptcha_sitekey: 'h', group_title: '测试群', user_id: 123, remaining_ms: 0 } }));
  await page.goto(`/verify/mini?task=${token}${telegramFragment('signed')}`);
  await expect(page.getByText('入群验证已超时，本次验证已关闭。')).toBeVisible();
  await expect.poll(() => page.evaluate(() => !!((window as any).cf && (window as any).h))).toBe(true);
  await page.evaluate(() => { (window as any).cf.callback('cf'); (window as any).h.callback('h'); });
  await expect(page.getByRole('button', { name: '提交验证' })).toBeDisabled();
});

test('Mini App without Telegram identity refuses verification', async ({ page }) => {
  await page.route('https://telegram.org/js/telegram-web-app.js*', route => route.fulfill({ contentType: 'application/javascript', body: telegramSDKFixture }));
  await page.goto(`/verify/mini?task=${token}#tgWebAppVersion=8.0`);
  await expect(page.locator('main[role=alert]')).toContainText('请本人从机器人私聊打开');
});

test('Mini App frame policy is scoped to the verification page', async ({ request }) => {
  const mini = await request.get('/verify/mini');
  expect(mini.headers()['content-security-policy']).toBe("frame-ancestors 'self' https://web.telegram.org");
  expect(mini.headers()['x-frame-options']).toBeUndefined();
  expect(mini.headers()['referrer-policy']).toBe('no-referrer');
  const home = await request.get('/');
  expect(home.headers()['content-security-policy'] ?? '').not.toContain('web.telegram.org');
});


test('verified task resumes without loading challenge scripts and ignores device clock offset', async ({ page }) => {
  await page.addInitScript(() => { Date.now = () => 1; });
  await page.route('https://telegram.org/js/telegram-web-app.js*', route => route.fulfill({ contentType: 'application/javascript', body: telegramSDKFixture }));
  let challengeRequests = 0;
  await page.route(/challenges\.cloudflare\.com|js\.hcaptcha\.com/, route => { challengeRequests++; return route.abort(); });
  await page.route('**/api/verify/mini/**', route => {
    if (route.request().url().endsWith('/config')) return route.fulfill({ json: { group_title: '测试群', user_id: 123, remaining_ms: 89000, recovery_pending: true } });
    expect(route.request().url()).toContain('/recover');
    expect(route.request().postDataJSON()).toEqual({token, init_data: 'signed'});
    return route.fulfill({ json: { success: true } });
  });
  await page.goto(`/verify/mini?task=${token}${telegramFragment('signed')}`);
  await expect(page.getByText(/剩余时间：01:2[0-9]/)).toBeVisible();
  await expect(page.getByText('验证已完成，请继续解除本次限制。')).toBeVisible();
  await page.getByRole('button', { name: '继续解除限制' }).click();
  await expect(page.getByText('入群验证已完成')).toBeVisible();
  expect(challengeRequests).toBe(0);
});
