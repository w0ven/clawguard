import {test,expect} from '@playwright/test';

for(const mode of ['decision','retry'] as const){
 test(`admin appeal ${mode}`,async({page})=>{
  await page.context().addCookies([{name:'cg_admin',value:'fixture',domain:'127.0.0.1',path:'/'}]);
  let status=mode==='retry'?'failed':'pending';let writes=0;
  const base={id:7,case_id:42,chat_id:-100,user_id:123,group_title:'测试群',decision:mode==='retry'?'approve':null,public_note:mode==='retry'?'已审核':'',execution_result:mode==='retry'?'待重试':'',reason:'用户理由',reference_url:'https://example.test/proof',turnstile_verified:true,hcaptcha_verified:true,action:'ban',case_result:'executed',explanation:'群规则处理',summary:'消息摘要',created_at:'2026-10-31T06:00:00Z',case_notification_state:'sent',result_notification_state:''};
  await page.route('**/*',r=>new URL(r.request().url()).hostname==='127.0.0.1'?r.continue():r.abort());
  await page.route('**/api/**',r=>{const path=new URL(r.request().url()).pathname;const method=r.request().method();if(path==='/api/admin/groups')return r.fulfill({json:{groups:[{chat_id:-100,title:'测试群'}]}});if(path==='/api/admin/appeals')return r.fulfill({json:{appeals:[{...base,status}],pending_count:status==='pending'?1:0,has_more:false}});if(path==='/api/admin/appeals/7'&&method==='GET')return r.fulfill({json:{appeal:{...base,status}}});if(path.startsWith('/api/admin/appeals/7/')&&method==='POST'){writes++;status='completed';return r.fulfill({json:{appeal:{...base,status,decision:'approve',public_note:'管理员说明',execution_result:'原封禁已解除'}}})}return r.fulfill({status:404,json:{error:'unexpected'}})});
  await page.goto('/appeals');await expect(page.getByRole('heading',{name:'申诉管理'})).toBeVisible();await page.getByRole('button',{name:/APL-7/}).click();await expect(page.getByText('用户理由：用户理由')).toBeVisible();await expect(page.getByText('两项安全验证：均已通过')).toBeVisible();
  if(mode==='decision'){await expect(page.getByRole('button',{name:'通过并核查处理'})).toBeDisabled();await page.getByLabel('公开处理说明（必填）').fill('管理员说明');await page.getByRole('button',{name:'通过并核查处理'}).click()}else await page.getByRole('button',{name:'重试处理或恢复结算'}).click();
  await expect(page.getByText('实际结果：原封禁已解除')).toBeVisible();expect(writes).toBe(1);
 });
}
