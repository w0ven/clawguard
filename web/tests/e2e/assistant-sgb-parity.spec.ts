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
 const roles=page.locator('.glass-panel').filter({has:page.getByRole('heading',{name:'模型角色',exact:true})});
 // Ordered model references replace the former single fallback selector.
 await roles.getByLabel('决策负载策略',{exact:true}).selectOption('weighted');
 await roles.getByLabel('决策模型 1',{exact:true}).selectOption('fixture:tools');
 for(const [i,ref] of ['fixture:backup','fixture:text'].entries()){
  await roles.getByRole('button',{name:'添加决策备用模型',exact:true}).click();
  await roles.getByLabel(`决策模型 ${i+2}`,{exact:true}).selectOption(ref);
 }
 for(const [i,weight] of ['5','3','2'].entries())await roles.getByLabel(`决策权重 ${i+1}`,{exact:true}).fill(weight);
 await roles.getByLabel('决策并发 2',{exact:true}).fill('4');
 await roles.getByLabel('决策端点超时 2',{exact:true}).fill('17000');
 await roles.getByLabel('决策冷却 2',{exact:true}).fill('41');
 await roles.getByLabel('决策温度',{exact:true}).fill('0.2');
 await roles.getByLabel('决策最大输出',{exact:true}).fill('512');
 const policyBeforeGlobal=structuredClone(api.policies[-1001]);
 expect(api.writes).toHaveLength(1); // Neither role edits nor Prompt edits auto-save.
 await page.getByLabel('人格').fill('自定义人格覆盖');
 await page.getByRole('button',{name:'保存全局设置'}).click();
 await expect(page.getByText('全局助手设置已保存',{exact:true})).toBeVisible();
 expect(api.writes.some(w=>w.path==='/api/admin/assistant/prompts'&&w.body.prompts.persona==='自定义人格覆盖')).toBe(true);
 expect(api.writes.map(w=>({path:w.path,method:w.method}))).toEqual([
  {path:'/api/admin/groups/-1001/assistant',method:'PUT'},
  {path:'/api/admin/assistant/global',method:'PUT'},
  {path:'/api/admin/assistant/prompts',method:'PUT'},
 ]);
 expect(api.writes[1].body).toMatchObject({expected_version:1,model_roles:{decision:{model_ref:'fixture:tools',fallbacks:['fixture:backup','fixture:text'],strategy:'weighted',temperature:0.2,max_tokens:512,model_options:{'fixture:tools':{weight:5},'fixture:backup':{weight:3,max_concurrency:4,timeout_ms:17000,cooldown_duration_sec:41},'fixture:text':{weight:2}}}}});
 expect(api.policies[-1001]).toEqual(policyBeforeGlobal);
 for(const [i,ref] of ['fixture:tools','fixture:backup','fixture:text'].entries())await expect(roles.getByLabel(`决策模型 ${i+1}`,{exact:true})).toHaveValue(ref);
 await expect(roles.getByLabel('决策权重 2',{exact:true})).toHaveValue('3');
 await expect(roles.getByLabel('决策端点超时 2',{exact:true})).toHaveValue('17000');
 await expect(page.getByText('处罚')).toHaveCount(0);
 await expect(page.getByText('投票封禁')).toHaveCount(0);
 await expect(page.getByText('vote_ban')).toHaveCount(0);
});
