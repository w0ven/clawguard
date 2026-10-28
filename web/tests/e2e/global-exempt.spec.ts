import { test, expect, login } from './ui-fixtures';

test('global exemptions add, revoke, validation and server error', async ({page}) => {
  let entries: Array<{user_id:string;note:string;expires_at:string|null;created_at:string}> = [];
  const writes: Array<{method:string; id:string; csrf:string|undefined; body?:any}> = [];
  let fail = false;
  await page.route('**/api/admin/global-exempt-users**', async route => {
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
  await page.goto('/global-exempt-users');
  await expect(page.getByRole('heading',{name:'全局用户豁免'})).toBeVisible();
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
});
