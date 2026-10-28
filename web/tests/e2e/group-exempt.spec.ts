import { test, expect, login, draftGroup, field } from './ui-fixtures';

test('group exemptions add, revoke, validation and server error', async ({page}) => {
  let entries: Array<{user_id:string;note:string;expires_at:string|null;created_at:string}> = [];
  const writes: Array<{method:string; id:string; csrf:string|undefined; body?:any}> = [];
  let fail = false;
  await page.route('**/api/admin/groups/-1001/exempt-users**', async route => {
    const req = route.request(); const id = new URL(req.url()).pathname.split('/').at(-1)!;
    if (req.method() === 'GET') return route.fulfill({json:{users:entries}});
    writes.push({method:req.method(),id,csrf:req.headers()['x-csrf-token'],body:req.method()==='PUT'?req.postDataJSON():undefined});
    if (fail) return route.fulfill({status:500,json:{error:'测试保存失败'}});
    if (req.method()==='PUT') entries=[{user_id:id,note:req.postDataJSON().note,expires_at:req.postDataJSON().expires_at,created_at:new Date().toISOString()}];
    if (req.method()==='DELETE') { entries=[]; return route.fulfill({status:204}); }
    return route.fulfill({json:{ok:true}});
  });
  await login(page);
  await page.context().addCookies([{name:'cg_csrf',value:'fixture-csrf',domain:'127.0.0.1',path:'/'}]);
  await page.goto('/groups/-1001');
  await expect(page.getByText('群信息',{exact:true})).toBeVisible();
  await expect(page.getByRole('heading',{name:'本群用户豁免'})).toHaveCount(0);
  await page.getByRole('button',{name:'用户豁免',exact:true}).click();
  await expect(page.getByRole('heading',{name:'本群用户豁免'})).toBeVisible();
  await page.getByLabel('Telegram 用户 ID').fill('not-a-number');
  await page.getByRole('button',{name:'保存豁免'}).click();
  await expect(page.locator('p[role=alert]')).toContainText('正整数');
  expect(writes).toHaveLength(0);
  await page.getByLabel('Telegram 用户 ID').fill('12345678901234567');
  await page.getByLabel('备注').fill('测试备注');
  await page.getByRole('button',{name:'保存豁免'}).click();
  await expect(page.getByText('12345678901234567')).toBeVisible();
  expect(writes[0]).toEqual({method:'PUT',id:'12345678901234567',csrf:'fixture-csrf',body:{note:'测试备注',expires_at:null}});
  page.once('dialog',d=>d.accept());
  await page.getByRole('button',{name:'撤销 12345678901234567'}).click();
  await expect(page.getByText('暂无豁免用户')).toBeVisible();
  expect(writes[1].method).toBe('DELETE');
  fail=true;
  await page.getByLabel('Telegram 用户 ID').fill('22');
  await page.getByRole('button',{name:'保存豁免'}).click();
  await expect(page.locator('p[role=alert]')).toContainText('测试保存失败');
  await expect(page.getByLabel('Telegram 用户 ID')).toHaveValue('22');
  await page.getByRole('button',{name:'群概览',exact:true}).first().click();
  await expect(page.getByText('群信息',{exact:true})).toBeVisible();
  await expect(page.getByRole('heading',{name:'本群用户豁免'})).toHaveCount(0);
});


test('exemption tab preserves policy draft and is absent from global settings', async ({ page }) => {
  let listRequests = 0;
  await page.route('**/api/admin/groups/-1001/exempt-users**', async (route) => {
    listRequests++;
    await route.fulfill({ json: { users: [], total: 0, has_more: false } });
  });
  await login(page);
  await page.goto('/groups/-1001');
  await expect(page.getByText('群信息', { exact: true })).toBeVisible();
  expect(listRequests).toBe(0);
  await draftGroup(page);
  await page.getByRole('button', { name: '用户豁免', exact: true }).click();
  await expect(page.getByRole('heading', { name: '本群用户豁免' })).toBeVisible();
  await expect.poll(() => listRequests).toBeGreaterThan(0);
  await expect(page.getByRole('button', { name: '保存策略', exact: true })).toBeEnabled();
  await page.getByRole('button', { name: '入群与资料', exact: true }).click();
  await expect(page.getByRole('heading', { name: '本群用户豁免' })).toHaveCount(0);
  await page.getByRole('button', { name: '展开详细设置（4 项）' }).first().click();
  await expect(field(page, '超时时长 (秒)').locator('input[type=number]')).toHaveValue('321');
  page.once('dialog', (dialog) => dialog.accept());
  await page.goto('/groups/global-config');
  await expect(page.getByRole('button', { name: '用户豁免', exact: true })).toHaveCount(0);
  await expect(page.getByRole('heading', { name: '本群用户豁免' })).toHaveCount(0);
});
