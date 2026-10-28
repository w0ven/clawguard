import { test, expect, login } from './ui-fixtures';

test.use({timezoneId:'America/New_York'});

test('exemption search pagination and safe expiry editing', async ({page}) => {
 const expiry='2035-04-15T08:23:45.123456Z';
 let entries=Array.from({length:21},(_,i)=>({user_id:String(i+100),note:i===0?'percent%_':'普通',expires_at:i===0?expiry:null,created_at:'2026-01-01T00:00:00Z'}));
 const writes:any[]=[];
 await page.route('**/api/admin/groups/-1001/exempt-users**',async route=>{
  const req=route.request(),url=new URL(req.url());
  if(req.method()==='GET'){
   const search=url.searchParams.get('search')||'', status=url.searchParams.get('status')||'all',offset=Number(url.searchParams.get('offset')||0),limit=Number(url.searchParams.get('limit')||20);
   if(search==='old') await new Promise(r=>setTimeout(r,300));
   const rows=entries.filter(x=>(x.note.includes(search)||x.user_id.includes(search))&&(status!=='expired'));
   return route.fulfill({json:{users:rows.slice(offset,offset+limit),total:rows.length,has_more:offset+limit<rows.length}});
  }
  const id=url.pathname.split('/').at(-1)!;
  if(req.method()==='PUT'){const body=req.postDataJSON();writes.push(body);entries=entries.map(x=>x.user_id===id?{...x,...body}:x);return route.fulfill({json:{ok:true}});}
  entries=entries.filter(x=>x.user_id!==id);return route.fulfill({status:204});
 });
 await login(page);await page.goto('/groups/-1001');
 await expect(page.getByText('共 21 条 · 第 1 页')).toBeVisible();
 await page.getByRole('button',{name:'编辑 100'}).click();
 await expect(page.getByLabel('Telegram 用户 ID')).toHaveValue('100');
 await page.getByLabel('备注',{exact:true}).fill('updated');
 await page.getByRole('button',{name:'保存豁免'}).click();
 await expect.poll(()=>writes.length).toBe(1);expect(writes[0]).toEqual({note:'updated',expires_at:expiry});
 await page.getByRole('button',{name:'编辑 100'}).click();await page.getByRole('button',{name:'取消编辑'}).click();await expect(page.getByLabel('Telegram 用户 ID')).toHaveValue('');
 await page.getByRole('button',{name:'编辑 100'}).click();await page.getByLabel('到期时间').fill('2035-04-15T05:23:45');await page.getByRole('button',{name:'保存豁免'}).click();await expect.poll(()=>writes.length).toBe(2);expect(writes[1].expires_at).toBe('2035-04-15T09:23:45.000Z');
 await page.getByLabel('搜索豁免').fill('old');await page.getByLabel('搜索豁免').fill('updated');await expect(page.getByRole('button',{name:'编辑 100'})).toBeVisible();await page.waitForTimeout(400);await expect(page.getByRole('button',{name:'编辑 100'})).toBeVisible();
 await page.getByLabel('搜索豁免').fill('');await page.getByRole('button',{name:'下一页'}).click();await expect(page.getByText('共 21 条 · 第 2 页')).toBeVisible();
 page.once('dialog',d=>d.accept());await page.getByRole('button',{name:'撤销 120'}).click();await expect(page.getByText('共 20 条 · 第 1 页')).toBeVisible();
 await page.getByLabel('豁免状态').selectOption('expired');await expect(page.getByText('暂无豁免用户')).toBeVisible();
});


test('switching groups cannot show old pending response or edit state',async ({page})=>{
 await page.route('**/api/admin/groups/-1001/exempt-users**',async route=>{await new Promise(r=>setTimeout(r,250));await route.fulfill({json:{users:[{user_id:'311',note:'old group',expires_at:null,created_at:'2026-01-01T00:00:00Z'}],total:1,has_more:false}}).catch(()=>{});});
 await page.route('**/api/admin/groups/-1002/exempt-users**',async route=>route.fulfill({json:{users:[{user_id:'422',note:'new group',expires_at:null,created_at:'2026-01-01T00:00:00Z'}],total:1,has_more:false}}));
 await login(page);await page.goto('/groups/-1001');await expect(page.getByRole('button',{name:'编辑 311'})).toBeVisible();await page.getByRole('button',{name:'编辑 311'}).click();
 await page.getByLabel('搜索豁免').fill('delayed');await page.goto('/groups/-1002');await expect(page.getByRole('button',{name:'编辑 422'})).toBeVisible();await page.waitForTimeout(400);await expect(page.getByText('old group')).toHaveCount(0);await expect(page.getByLabel('Telegram 用户 ID')).toHaveValue('');
});
