import {mini} from './ui-fixtures';
import {test,expect,openAssistant,tab,groups,deferred,login,tools} from './assistant-fixtures';

test('entry navigation group choice deep link default disabled and 7d truth',async({page,api})=>{
 await login(page);await page.goto('/groups/-1001');await page.getByRole('link',{name:'打开群助手',exact:true}).click();await expect(page).toHaveURL(/\/groups\/-1001\/assistant$/);await expect(page.getByText('实际状态',{exact:true})).toBeVisible();
 await expect(page.getByText('这个群现在还不能回复',{exact:true})).toBeVisible();await expect(page.getByText('7 天',{exact:true})).toBeVisible();await expect(page.getByText(/历史默认政策：7 天/)).toBeVisible();await expect(page.getByText(/不删除 Telegram 远端原文/)).toBeVisible();expect(api.writes).toEqual([]);
 await page.getByRole('link',{name:'群助手',exact:true}).filter({visible:true}).first().click();await expect(page).toHaveURL(/\/assistant$/);await page.getByRole('button',{name:new RegExp(groups[1].title)}).click();await expect(page).toHaveURL(/-1002\/assistant$/);await expect(page.getByText(groups[1].title,{exact:true})).toBeVisible();
});
for(const code of [401,403,404,500])test(`deep link HTTP ${code} and retry`,async({page,api})=>{
 await login(page);api.rules.push({match:/\/groups\/-1001\/assistant$/,status:code,once:true});await page.goto('/groups/-1001/assistant');
 if(code===401){await expect(page).toHaveURL(/\/$/);await expect(page.locator('h1')).toHaveText('ClawGuard');return;}
 await expect(page.getByText(code===403?'没有该群的管理范围':code===404?'群组不存在或已停用':'群助手加载失败',{exact:true})).toBeVisible();await page.getByRole('button',{name:'重试',exact:true}).click();await expect(page.getByText('实际状态',{exact:true})).toBeVisible();await expect.soft(page.getByLabel('切换群组')).toBeVisible();
});
for(const state of ['loading','empty','403'])test(`group picker ${state}`,async({page,api})=>{
 await login(page);const gate=deferred();if(state==='loading')api.rules.push({match:/\/api\/admin\/groups$/,wait:gate.wait,once:true});else api.rules.push({match:/\/api\/admin\/groups$/,status:state==='403'?403:200,body:state==='empty'?{groups:[]}:undefined});
 try{await page.goto('/assistant');if(state==='loading'){await expect(page.getByText('正在加载可管理群组…')).toBeVisible();gate.release();await expect(page.getByRole('button',{name:new RegExp(groups[0].title)})).toBeVisible();}else await expect(page.getByText(state==='empty'?'没有可管理群组':'没有群管理权限',{exact:true})).toBeVisible();}finally{gate.release();}
});

test('settings select model enables chat and sends the primary model reference without endpoint editing',async({page,api})=>{
 await openAssistant(page);await tab(page,'怎么说话');await expect(page.getByRole('switch',{name:'启用聊天',exact:true})).not.toBeChecked();await expect(page.getByRole('switch',{name:'启用普通群聊自动学习'})).not.toBeChecked();
 const chat=page.getByLabel('聊天模型',{exact:true}),learn=page.getByLabel('学习模型',{exact:true});await expect(chat.locator('option[value="fixture:text"]')).toHaveCount(1);await expect(chat.locator('option[value="fixture:no-tools"]')).toHaveCount(1);await expect(chat.locator('option[value="fixture:disabled"]')).toHaveCount(0);await chat.selectOption('fixture:no-tools');await page.getByRole('switch',{name:'启用聊天',exact:true}).click();await expect(page.getByText(/确定不能作为聊天模型/)).toBeVisible();await expect(page.getByRole('switch',{name:'启用聊天',exact:true})).not.toBeChecked();
 await chat.selectOption('fixture:tools');await learn.selectOption('fixture:text');await page.getByRole('switch',{name:'启用聊天',exact:true}).click();await expect(page.getByRole('switch',{name:'启用聊天',exact:true})).toBeChecked();await page.getByLabel('唤起方式').selectOption('mention_only');
 // These remain editable compatibility values, not a promise to force follow-up
 // replies. The SGB entry consults the proactive/mention switches instead.
 await expect(page.getByLabel('旧追问窗口（秒）',{exact:false})).toHaveValue(String(api.policies[-1001].followup_window_sec));
 await expect(page.getByLabel('旧追问轮次（保留值）',{exact:false})).toHaveValue(String(api.policies[-1001].max_followup_turns));
 await expect(page.getByText('保留旧存储值；当前由决策处理，不再强制回复',{exact:true})).toBeVisible();
 await expect(page.getByText('不绕过主动聊天开关',{exact:true})).toBeVisible();
 for(const [label,value] of [['旧追问窗口（秒）','600'],['旧追问轮次（保留值）','9'],['历史上下文条数','44']])await page.getByLabel(label,{exact:false}).fill(value);
 await page.getByLabel('网页域名白名单').fill('docs.example.test');
 await tab(page,'主动与风格');await page.getByRole('switch',{name:'有把握才插一句'}).click();await expect(page.getByRole('switch',{name:'有把握才插一句'})).toBeChecked();await page.getByRole('switch',{name:'有把握才插一句'}).click();
 await tab(page,'模型与负载（高级）');await page.getByText('备用模型、排队与回答随机程度',{exact:true}).click();for(const [label,value] of [['回答随机程度','0'],['原文保留天数','9'],['最大本地队列深度','0'],['最大排队等待（秒）','22']])await page.getByLabel(label,{exact:false}).fill(value);await page.getByLabel('默认系统指令').fill('Fixture system only');await page.getByRole('button',{name:'保存设置'}).click();await expect(page.getByText('群助手设置已保存',{exact:true})).toBeVisible();
 expect(api.writes).toHaveLength(1);expect(api.writes[0]).toMatchObject({path:'/api/admin/groups/-1001/assistant',method:'PUT',csrf:'fixture-csrf',body:{expected_version:0,chat_enabled:true,learning_enabled:false,trigger_mode:'mention_only',followup_window_sec:600,max_followup_turns:9,history_limit:44,temperature:0,retention_days:9,chat_model_ref:'fixture:tools',learning_model_ref:'fixture:text',max_queue_depth:0,max_queue_wait_sec:22,system_prompt:'Fixture system only',allow_domains:['docs.example.test'],tool_allowlist:tools}});expect(api.writes[0].body).not.toHaveProperty('model_pool');
 expect(api.writes[0].body).toMatchObject({proactive_interject_enabled:false,proactive_cold_topic_enabled:false});
 await tab(page,'回复与媒体');
 await expect(page.getByLabel('旧追问窗口（秒）',{exact:false})).toHaveValue('600');
 await expect(page.getByLabel('旧追问轮次（保留值）',{exact:false})).toHaveValue('9');
 await page.getByRole('switch',{name:'启用普通群聊自动学习'}).click();await page.getByRole('button',{name:'保存设置'}).click();await expect.poll(()=>api.writes.length).toBe(2);
 expect(api.writes[1].body).toMatchObject({expected_version:1,chat_enabled:true,learning_enabled:true,chat_model_ref:'fixture:tools',followup_window_sec:600,max_followup_turns:9,max_queue_depth:0,max_queue_wait_sec:22});
 expect(api.writes.every(w=>w.path==='/api/admin/groups/-1001/assistant'&&w.method==='PUT')).toBe(true);
 expect(api.writes.some(w=>w.path.endsWith('/config')||w.path.includes('/global-config'))).toBe(false);
});

for(const code of [409,500])test(`settings ${code} retains draft and allows retry`,async({page,api})=>{
 await openAssistant(page);await tab(page,'模型与负载（高级）');await page.getByText('备用模型、排队与回答随机程度',{exact:true}).click();const input=page.getByLabel('默认系统指令');await input.fill('保留这份设置草稿');api.rules.push({match:/\/assistant$/,method:'PUT',status:code,once:true});await page.getByRole('button',{name:'保存设置'}).click();await expect(page.getByText(code===409?/设置版本冲突：草稿已保留/:/Fixture injected 500/).first()).toBeVisible();await expect(input).toHaveValue('保留这份设置草稿');await expect(page.getByRole('button',{name:'保存设置'})).toBeEnabled();expect(api.writes[0].body.expected_version).toBe(0);
 let accept=false;page.on('dialog',async d=>accept?d.accept():d.dismiss());await page.getByRole('button',{name:'取消草稿'}).click();await expect(input).toHaveValue('保留这份设置草稿');accept=true;await page.getByRole('button',{name:'取消草稿'}).click();await expect(input).toHaveValue('');expect(api.writes).toHaveLength(1);
});

test('settings saving keyboard second edit never loses the second snapshot',async({page,api})=>{
 await openAssistant(page);await tab(page,'模型与负载（高级）');await page.getByText('备用模型、排队与回答随机程度',{exact:true}).click();const input=page.getByLabel('默认系统指令');await input.fill('snapshot one');const gate=deferred();api.rules.push({match:/\/assistant$/,method:'PUT',wait:gate.wait,once:true});
 try{await page.getByRole('button',{name:'保存设置'}).click();await expect.poll(()=>api.writes.length).toBe(1);await expect(input).toBeDisabled();const edited=await input.inputValue();await expect(page.getByRole('button',{name:'保存中…'})).toBeDisabled();gate.release();await expect(page.getByText('群助手设置已保存',{exact:true})).toBeVisible();await expect(input).toHaveValue(edited);}finally{gate.release();}
});

for(const action of ['tab','group','link','back','miniBack'])test(`dirty navigation ${action} cancel URL content then accept`,async({page,api})=>{
 if(action==='miniBack')await mini(page);await login(page);await page.goto('/assistant');await page.getByRole('button',{name:new RegExp(groups[0].title)}).click();await expect(page.getByLabel('切换群组')).toBeVisible();await tab(page,'怎么说话');const input=page.getByLabel('聊天模型',{exact:true});await input.selectOption('fixture:tools');let accept=false;const messages:string[]=[];page.on('dialog',async d=>{messages.push(d.message());accept?await d.accept():await d.dismiss();});
 const act=async()=>{if(action==='tab')await tab(page,'群记忆');else if(action==='group')await page.getByLabel('切换群组').selectOption('-1002');else if(action==='link')await page.getByRole('link',{name:'群管理',exact:true}).filter({visible:true}).first().click();else if(action==='back')await page.goBack();else await page.evaluate(()=>(window as any).__miniBack());};await act();await page.waitForTimeout(400);await expect(page).toHaveURL(/-1001\/assistant$/);await expect(input).toHaveValue('fixture:tools');expect(messages).toHaveLength(1);accept=true;await act();if(action==='tab')await expect(page.getByRole('button',{name:'新增基础事实'})).toBeVisible();else if(action==='group'){await expect(page).toHaveURL(/-1002\/assistant$/);await expect(page.getByText(groups[1].title,{exact:true})).toBeVisible();}else if(action==='link'){await expect(page).toHaveURL(/\/groups$/);}else{await expect(page).toHaveURL(/\/assistant$/);}expect(api.writes).toEqual([]);
});
