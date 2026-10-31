import { test, expect, login, group, policy } from './ui-fixtures';

const item={id:9,chat_id:-1001,name:'Fixture scheduled',schedule_type:'interval',interval_minutes:60,daily_times:[],timezone:'Asia/Shanghai',content:'fixture',buttons:[],auto_delete_seconds:0,enabled:true,status:'active',last_run_at:null,last_message_id:null,last_error:null,last_skip_reason:null,next_run_at:null,created_at:'2026-09-01T00:00:00Z',updated_at:'2026-09-01T00:00:00Z'};

async function showScheduled(page: import('@playwright/test').Page) {
 await page.goto('/groups/-1001');await page.getByRole('button',{name:'群运营',exact:true}).click();await page.getByRole('button',{name:'定时消息',exact:true}).click();await expect(page.getByText('Fixture scheduled',{exact:true})).toBeVisible();
}

test('lost response and refresh reuse operation key; only sent starts a new one',async({page,mock})=>{
 await login(page);const keys:string[]=[];let release!:()=>void;const pending=new Promise<void>(r=>release=r);
 await page.route('**/api/admin/groups/-1001/scheduled-messages',r=>r.fulfill({json:{scheduled_messages:[item],limit:20}}));
 await page.route('**/api/admin/groups/-1001/scheduled-messages/9/run-now',async r=>{keys.push(r.request().headers()['idempotency-key']);if(keys.length===1){await pending;return r.abort('failed')}if(keys.length===2)return r.fulfill({json:{status:'unknown'}});return r.fulfill({json:{status:'sent',message_id:123}})});
 await showScheduled(page);const send=page.getByRole('button',{name:'立即试发'});await send.click();await expect.poll(()=>keys.length).toBe(1);await expect(send).toBeDisabled();release();await expect(page.getByText(/请核对发送历史/)).toBeVisible();
 await page.reload();await page.getByRole('button',{name:'群运营',exact:true}).click();await page.getByRole('button',{name:'定时消息',exact:true}).click();await send.click();await expect(page.getByText(/试发结果待确认/)).toBeVisible();expect(keys[1]).toBe(keys[0]);
 await send.click();await expect(page.getByText('试发成功',{exact:true})).toBeVisible();expect(keys[2]).toBe(keys[0]);await send.click();await expect.poll(()=>keys.length).toBe(4);expect(keys[3]).not.toBe(keys[0]);const expected=mock.console.indexOf('Failed to load resource: net::ERR_FAILED');expect(expected).toBeGreaterThanOrEqual(0);mock.console.splice(expected,1);
});

test('blocked browser storage fails closed before dispatch',async({page})=>{
 await login(page);await page.addInitScript(()=>{const original=Storage.prototype.setItem;Storage.prototype.setItem=function(k,v){if(k.startsWith('clawguard-scheduled-run:'))throw Error('blocked');return original.call(this,k,v)}});let writes=0;
 await page.route('**/api/admin/groups/-1001/scheduled-messages',r=>r.fulfill({json:{scheduled_messages:[item],limit:20}}));await page.route('**/api/admin/groups/-1001/scheduled-messages/9/run-now',r=>{writes++;return r.fulfill({json:{status:'sent'}})});
 await showScheduled(page);await page.getByRole('button',{name:'立即试发'}).click();await expect(page.getByText(/无法保存试发操作编号/)).toBeVisible();expect(writes).toBe(0);
});

for(const state of ['dispatching','failed'] as const)test(`run-now ${state} is not reported as success and reuses key`,async({page})=>{
 await login(page);const keys:string[]=[];await page.route('**/api/admin/groups/-1001/scheduled-messages',r=>r.fulfill({json:{scheduled_messages:[item],limit:20}}));await page.route('**/api/admin/groups/-1001/scheduled-messages/9/run-now',r=>{keys.push(r.request().headers()['idempotency-key']);return r.fulfill({json:{status:state}})});
 await showScheduled(page);const send=page.getByRole('button',{name:'立即试发'});await send.click();await expect(page.getByText(state==='failed'?/试发失败，请查看发送历史/:/试发仍在处理中/)).toBeVisible();await expect(page.getByText('试发成功',{exact:true})).toHaveCount(0);await send.click();await expect.poll(()=>keys.length).toBe(2);expect(keys[1]).toBe(keys[0]);
});

test('confirmed failed can explicitly create a new intent after reload; uncertain retry keeps new key',async({page,mock})=>{
 await login(page);const keys:string[]=[];await page.route('**/api/admin/groups/-1001/scheduled-messages',r=>r.fulfill({json:{scheduled_messages:[item],limit:20}}));
 await page.route('**/api/admin/groups/-1001/scheduled-messages/9/run-now',r=>{const req=r.request();expect(req.method()).toBe('POST');expect(req.postData()).toBeNull();const key=req.headers()['idempotency-key'];expect(key).toMatch(/^[A-Za-z0-9_-]{32,64}$/);keys.push(key);return r.fulfill({json:{status:keys.length===1?'failed':'unknown'}})});
 await showScheduled(page);await page.getByRole('button',{name:'立即试发'}).click();await expect(page.getByRole('button',{name:'重新尝试'})).toBeVisible();await page.reload();await page.getByRole('button',{name:'群运营',exact:true}).click();await page.getByRole('button',{name:'定时消息',exact:true}).click();await expect(page.getByRole('button',{name:'重新尝试'})).toBeVisible();
 page.once('dialog',d=>d.dismiss());await page.getByRole('button',{name:'重新尝试'}).click();expect(keys).toHaveLength(1);page.once('dialog',d=>d.accept());await page.getByRole('button',{name:'重新尝试'}).click();await expect(page.getByText(/试发结果待确认/)).toBeVisible();expect(keys[1]).not.toBe(keys[0]);await expect(page.getByRole('button',{name:'重新尝试'})).toHaveCount(0);await page.getByRole('button',{name:'立即试发'}).click();await expect.poll(()=>keys.length).toBe(3);expect(keys[2]).toBe(keys[1]);
});

test('malformed sent acknowledgement and conflict never clear key',async({page,mock})=>{
 await login(page);const keys:string[]=[];await page.route('**/api/admin/groups/-1001/scheduled-messages',r=>r.fulfill({json:{scheduled_messages:[item],limit:20}}));await page.route('**/api/admin/groups/-1001/scheduled-messages/9/run-now',r=>{keys.push(r.request().headers()['idempotency-key']);return keys.length===1?r.fulfill({json:{status:'sent'}}):r.fulfill({status:409,json:{error:'配置已变'}})});
 await showScheduled(page);const send=page.getByRole('button',{name:'立即试发'});await send.click();await expect(page.getByText(/试发结果待确认/)).toBeVisible();await expect(page.getByRole('button',{name:'重新尝试'})).toHaveCount(0);await send.click();await expect(page.getByText(/配置已变/)).toBeVisible();expect(keys[1]).toBe(keys[0]);const index=mock.console.findIndex(s=>s.includes('409'));if(index>=0)mock.console.splice(index,1);
});
