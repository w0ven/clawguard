import { test, expect, login } from './ui-fixtures';

const decisions = [12, 13].map(id => ({id,chat_id:-1001,user_id:555+id,message_text:'fixture',verdict:'spam',category:'spam',scene:'message',action_taken:'mute',confidence:0.99,created_at:'2026-10-28T00:00:00Z'}));

test('AI mute retry warning and partial batch are visible', async ({page}) => {
  let failures = true;
  await page.route('**/api/admin/ai-decisions**', async route => {
    if (route.request().method() === 'GET') return route.fulfill({json:{decisions}});
    if (route.request().url().endsWith('/12') && failures) return route.fulfill({json:{unmute_performed:false,side_effect_warnings:['解除禁言失败，请重试']}});
    return route.fulfill({json:{unmute_performed:true,side_effect_warnings:[]}});
  });
  await login(page); await page.goto('/ai-review');
  await page.getByRole('button',{name:'误封',exact:true}).first().click();
  await expect(page.getByText('解除禁言失败，请重试')).toBeVisible();
  failures = false;
  await page.getByRole('button',{name:'误封',exact:true}).first().click();
  await expect(page.getByText('已解除禁言')).toBeVisible();
  failures = true;
  await page.getByRole('checkbox').first().check(); await page.getByRole('checkbox').nth(1).check();
  await page.getByRole('button',{name:'批量误封'}).click();
  await expect(page.getByText(/成功 1 条，部分失败 1 条，失败 0 条/)).toBeVisible();
});

test('trust status displays Telegram failure and can retry', async ({page}) => {
  let writes = 0;
  const item = {chat_id:-1001,user_id:123,username:'fixture_user',status:'banned',score:0.5,messages_checked:0,messages_clean:0,joined_at:'2026-10-28T00:00:00Z',updated_at:'2026-10-28T00:00:00Z'};
  await page.route('**/api/admin/user-trust**',async route => {
    if (route.request().method() === 'GET') return route.fulfill({json:{items:[item],total:1,has_more:false,counts:{all:1,banned:1}}});
    writes++;
    return route.fulfill({json:writes === 1 ? {trust:item,telegram_action_error:'fixture unban failure',sync_pending:true} : {trust:{...item,status:'trusted'}}});
  });
  await login(page); await page.goto('/trust');
  await page.getByRole('button',{name:'信任',exact:true}).click();
  await expect(page.getByText(/Telegram 同步失败，请重试/)).toBeVisible();
  await page.getByRole('button',{name:'信任',exact:true}).click();
  await expect(page.getByText('已更新',{exact:true})).toBeVisible();
  expect(writes).toBe(2);
});

test('AI review locks all actions while request is unresolved', async ({page}) => {
 let writes=0;let release!:()=>void;const pending=new Promise<void>(r=>release=r);
 await page.route('**/api/admin/ai-decisions**',async route=>{if(route.request().method()==='GET')return route.fulfill({json:{decisions}});writes++;await pending;return route.fulfill({status:500,json:{error:'操作失败'}})});
 await login(page);await page.goto('/ai-review');await page.getByRole('checkbox').first().check();await page.getByRole('button',{name:'批量误封'}).click();await expect(page.getByRole('button',{name:'批量正确'})).toBeDisabled();await expect(page.getByRole('button',{name:'误封',exact:true}).first()).toBeDisabled();expect(writes).toBe(1);release();await expect(page.getByText(/失败 1 条/)).toBeVisible();
});

test('trust actions cannot overlap while Telegram synchronization is pending', async ({page})=>{
 let writes=0;let release!:()=>void;const pending=new Promise<void>(r=>release=r);const item={chat_id:-1001,user_id:123,username:'fixture_user',status:'banned',score:0.5,messages_checked:0,messages_clean:0,joined_at:'2026-10-28T00:00:00Z',updated_at:'2026-10-28T00:00:00Z'};
 await page.route('**/api/admin/user-trust**',async route=>{if(route.request().method()==='GET')return route.fulfill({json:{items:[item],total:1,has_more:false,counts:{all:1,banned:1}}});writes++;await pending;return route.fulfill({json:{telegram_action_error:'无法同步'}})});
 await login(page);await page.goto('/trust');await page.getByRole('button',{name:'信任',exact:true}).click();await expect(page.getByRole('button',{name:'封禁',exact:true})).toBeDisabled();await expect.poll(()=>writes).toBe(1);release();await expect(page.getByText(/Telegram 同步失败，请重试/)).toBeVisible();await expect(page.getByRole('button',{name:'信任',exact:true})).toBeEnabled();
});
