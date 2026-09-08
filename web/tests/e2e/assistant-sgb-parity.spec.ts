import {test,expect,openAssistant,tab} from './assistant-fixtures';

test('学语气 ID 名字和点选填入、tts_mode、角色回退、Prompt 保存，且没有处罚投票设置',async({page,api})=>{
 await openAssistant(page);
 await tab(page,'主动与风格');
 await expect(page.getByLabel('风格目标用户 ID')).toBeVisible();
 await expect(page.getByLabel('风格目标名称')).toBeVisible();
 await page.getByLabel('近期发言人').selectOption('7');
 await expect(page.getByLabel('风格目标用户 ID')).toHaveValue('7');
 await expect(page.getByLabel('风格目标名称')).toHaveValue('Fixture 成员');
 await tab(page,'回复与媒体');
 await page.getByLabel('语音模式').selectOption('always');
 await expect(page.getByLabel('语音模式')).toHaveValue('always');
 await page.getByRole('button',{name:'保存设置'}).click();
 await expect(page.getByText('群助手设置已保存',{exact:true})).toBeVisible();
 expect(api.writes[0].body).toMatchObject({tts_mode:'always',mimic_target_user_id:7,mimic_target_user_name:'Fixture 成员'});
 await tab(page,'全局');
 await expect(page.getByText('模型角色',{exact:true})).toBeVisible();
 await expect(page.getByLabel('决策回退')).toBeVisible();
 await page.getByLabel('人格').fill('自定义人格覆盖');
 await page.getByRole('button',{name:'保存全局设置'}).click();
 await expect(page.getByText('全局助手设置已保存',{exact:true})).toBeVisible();
 expect(api.writes.some(w=>w.path==='/api/admin/assistant/prompts'&&w.body.prompts.persona==='自定义人格覆盖')).toBe(true);
 await expect(page.getByText('处罚')).toHaveCount(0);
 await expect(page.getByText('投票封禁')).toHaveCount(0);
 await expect(page.getByText('vote_ban')).toHaveCount(0);
});
