import { test, expect, login } from './ui-fixtures';

test('legacy AI ban review warns that manual unban is required', async ({page}) => {
  await page.route('**/api/admin/ai-decisions**', async route => {
    if (route.request().method()==='PUT') return route.fulfill({json:{unban_performed:false,side_effect_warnings:['无法确认当前封禁归属，请使用手动解封']}});
    return route.fulfill({json:{decisions:[{id:12,chat_id:-1001,user_id:555,message_text:'fixture',verdict:'spam',category:'spam',scene:'message',action_taken:'ban',confidence:0.99,created_at:'2026-10-28T00:00:00Z'}]}});
  });
  await login(page); await page.goto('/ai-review');
  await page.getByRole('button',{name:'误封',exact:true}).click();
  await expect(page.getByText('无法确认当前封禁归属，请使用手动解封')).toBeVisible();
});
