import {test, expect} from '@playwright/test';

test('invalid group scope never becomes unrestricted admin', async ({page}) => {
 await page.context().addCookies([{name:'cg_admin',value:'fixture',domain:'127.0.0.1',path:'/'}]);
 let writes: any[]=[];
 await page.route('**/*',r=>new URL(r.request().url()).hostname==='127.0.0.1'?r.continue():r.abort());
 await page.route('**/api/**',r=>{const p=new URL(r.request().url()).pathname;if(p==='/api/auth/me')return r.fulfill({json:{admin:{id:1,role:'owner'}}});if(p==='/api/admin/groups')return r.fulfill({json:{groups:[]}});if(p==='/api/admin/appeals')return r.fulfill({json:{appeals:[],pending_count:0,has_more:false}});if(p==='/api/admin/admins'&&r.request().method()==='GET')return r.fulfill({json:{admins:[]}});if(p==='/api/admin/admins'){writes.push(r.request().postDataJSON());return r.fulfill({json:{ok:true}})}return r.fulfill({status:404,json:{error:'unexpected'}})});
 await page.goto('/admins');await page.getByRole('button',{name:'新增管理员'}).click();await page.getByPlaceholder('例如 123456789').fill('123456');const scope=page.getByPlaceholder('-1001234, -1005678');
 for(const invalid of ['abc',', ,','-1001234,abc','9007199254740993']){await scope.fill(invalid);await page.getByRole('button',{name:'保存',exact:true}).click();await expect(page.getByText('群 ID 格式无效；仅留空表示全部群').first()).toBeVisible();expect(writes).toHaveLength(0)}
 await scope.fill('-1001234, -1005678');await page.getByRole('button',{name:'保存',exact:true}).click();await expect.poll(()=>writes.length).toBe(1);expect(writes[0].group_scope).toEqual([-1001234,-1005678]);
});

for (const path of ['/groups/global-config','/prompt-editor']) test(`configuration cannot be edited before loading: ${path}`, async ({page})=>{
 await page.context().addCookies([{name:'cg_admin',value:'fixture',domain:'127.0.0.1',path:'/'}]);let release!:()=>void;const pending=new Promise<void>(r=>release=r);
 await page.route('**/*',r=>new URL(r.request().url()).hostname==='127.0.0.1'?r.continue():r.abort());
 await page.route('**/api/**',async r=>{const p=new URL(r.request().url()).pathname;if(p==='/api/admin/global-config'){await pending;return r.fulfill({json:{config:{ai:{message_rules:'远端规则',bio_rules:'远端简介'}},version:1}})}if(p==='/api/admin/llm/models')return r.fulfill({json:{models:[]}});if(p==='/api/admin/ai-providers')return r.fulfill({json:{providers:[]}});if(p==='/api/admin/appeals')return r.fulfill({json:{appeals:[],pending_count:0,has_more:false}});return r.fulfill({status:404,json:{error:'unexpected'}})});
 await page.goto(path);if(path==='/prompt-editor'){const rule=page.getByPlaceholder('例如：本群禁止讨论币圈、刷单、招聘；本群禁止所有外站链接；娱乐内容从宽处理。');await expect(rule).toBeDisabled();release();await expect(rule).toHaveValue('远端规则');await expect(rule).toBeEnabled()}else{await expect(page.getByText('正在加载全局策略')).toBeVisible();await expect(page.locator('fieldset').first()).toHaveAttribute('disabled','');await expect(page.locator('fieldset').first().locator('input, button, textarea, select').first()).toBeDisabled();release();await expect(page.locator('fieldset').first()).not.toHaveAttribute('disabled','')}
});

test('violation batch deduplicates user targets and prevents overlapping actions', async ({page})=>{
 await page.context().addCookies([{name:'cg_admin',value:'fixture',domain:'127.0.0.1',path:'/'}]);let writes=0;let release!:()=>void;const pending=new Promise<void>(r=>release=r);
 const records=[1,2].map(id=>({id,chat_id:-1001,user_id:123,username:null,rule:'fixture',matched:null,action:'ban',message_text:null,created_at:'2026-10-31T06:00:00Z'}));
 await page.route('**/*',r=>new URL(r.request().url()).hostname==='127.0.0.1'?r.continue():r.abort());
 await page.route('**/api/**',async r=>{const p=new URL(r.request().url()).pathname;if(p==='/api/admin/violations')return r.fulfill({json:{violations:records}});if(p==='/api/admin/warnings')return r.fulfill({json:{warnings:[]}});if(p==='/api/admin/groups')return r.fulfill({json:{groups:[]}});if(p==='/api/admin/appeals')return r.fulfill({json:{appeals:[],pending_count:0,has_more:false}});if(p==='/api/admin/unban'){writes++;await pending;return r.fulfill({status:204})}return r.fulfill({status:404,json:{error:'unexpected'}})});
 await page.goto('/violations');await page.getByRole('checkbox').nth(0).check();await page.getByRole('checkbox').nth(1).check();await page.getByRole('button',{name:'批量 unban'}).click();await expect(page.getByRole('button',{name:'清除警告'})).toBeDisabled();await expect.poll(()=>writes).toBe(1);release();await expect(page.getByText('成功 1 条，失败 0 条')).toBeVisible();
});

test('authorized group title blur saves and failed save preserves draft for retry', async ({page})=>{
 await page.context().addCookies([{name:'cg_admin',value:'fixture',domain:'127.0.0.1',path:'/'}]);let group={chat_id:-1001,title:'旧标题',authorized_at:'2026-10-31T06:00:00Z',authorized_by:1,enabled:true,notes:''};let writes:any[]=[];
 await page.route('**/*',r=>new URL(r.request().url()).hostname==='127.0.0.1'?r.continue():r.abort());
 await page.route('**/api/**',r=>{const p=new URL(r.request().url()).pathname;if(p==='/api/admin/appeals')return r.fulfill({json:{appeals:[],pending_count:0,has_more:false}});if(p==='/api/admin/authorized-groups')return r.fulfill({json:{groups:[group]}});if(p==='/api/admin/authorized-groups/-1001'&&r.request().method()==='PUT'){const body=r.request().postDataJSON();writes.push(body);if(writes.length===1)return r.fulfill({status:500,json:{error:'保存失败'}});group={...group,...body};return r.fulfill({json:{ok:true}})}return r.fulfill({status:404,json:{error:'unexpected'}})});
 await page.goto('/authorized-groups');const input=page.getByRole('table').getByRole('textbox');await expect(input).toHaveValue('旧标题');await input.fill('新标题');await input.blur();await expect(page.getByText('保存失败')).toBeVisible();await expect(input).toHaveValue('新标题');await expect(page.getByRole('switch')).toBeDisabled();await input.focus();await input.blur();await expect.poll(()=>writes.length).toBe(2);await expect(input).toHaveValue('新标题');await expect(page.getByRole('switch')).toBeEnabled();expect(writes.map(x=>x.title)).toEqual(['新标题','新标题']);
});
