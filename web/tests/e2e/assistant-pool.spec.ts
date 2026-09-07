import {test,expect,openAssistant,tab,configuredPool} from './assistant-fixtures';

test('selecting a chat model saves settings and lets the server create the primary endpoint',async({page,api})=>{
 await openAssistant(page);await tab(page,'怎么说话');await page.getByLabel('聊天模型',{exact:true}).selectOption('fixture:tools');page.once('dialog',d=>d.accept());await tab(page,'模型与负载（高级）');await page.getByText('备用模型、排队与回答随机程度',{exact:true}).click();await expect(page.getByText('主模型优先，忙时自动用备用',{exact:true})).toBeVisible();await expect(page.getByText('当前主模型',{exact:true})).toBeVisible();await expect(page.getByText('备用模型（可选）',{exact:true})).toBeVisible();await page.getByRole('button',{name:'保存设置'}).click();await expect(page.getByText('群助手设置已保存',{exact:true})).toBeVisible();expect(api.writes).toHaveLength(1);expect(api.writes[0].path).toBe('/api/admin/groups/-1001/assistant');expect(api.writes[0].body).toMatchObject({expected_version:0,chat_model_ref:'fixture:tools',chat_enabled:false});expect(api.writes.some(w=>w.path.endsWith('/model-pool'))).toBe(false);expect(api.pools[-1001].config.task_assignments.chat.primary).toBe('ep-auto-chat');
});

test('advanced view does not require endpoint or primary assignment fields',async({page,api})=>{
 api.pools[-1001]=configuredPool();await openAssistant(page);await tab(page,'模型与负载（高级）');await page.getByText('备用模型、排队与回答随机程度',{exact:true}).click();await expect(page.getByText('当前没有备用模型',{exact:false})).toHaveCount(0);await expect(page.getByText('Fixture backup',{exact:true})).toBeVisible();await expect(page.getByLabel('主端点')).toHaveCount(0);await expect(page.getByLabel('有序备用端点')).toHaveCount(0);await expect(page.getByRole('button',{name:/从.*添加端点|删除端点/})).toHaveCount(0);expect(api.writes).toEqual([]);
});

test('registry 403 keeps the model reference and shows the management hint',async({page,api})=>{
 api.policies[-1001].chat_model_ref='fixture:tools';api.rules.push({match:/\/llm\/models$/,status:403});await openAssistant(page);await tab(page,'怎么说话');await expect(page.getByText(/当前管理员没有查看模型清单的权限/).first()).toBeVisible();await expect(page.getByLabel('聊天模型',{exact:true})).toHaveValue('fixture:tools');expect(api.requests.filter(r=>r.path.includes('/llm/')).every(r=>r.path==='/api/admin/llm/models')).toBe(true);expect(api.writes).toEqual([]);
});

for(const state of ['empty','observed','403'])test(`runtime ${state} shows honest local status and no fake remote quota`,async({page,api})=>{
 if(state==='403'){api.rules.push({match:/\/status$/,status:403},{match:/\/dispatches\?/,status:403});}
 if(state==='observed'){api.status[-1001].endpoints_status=[{id:'ep-main',model_ref:'fixture:tools',model_label:'Fixture observed',role:'primary',provider_ref:'fixture',current_active:3,max_concurrency:3,is_full:true,status:'cooldown',cooldown_remaining_sec:12,last_error:'Fixture 429 trimmed',supports_tools:true,remote_quota_observed:'unknown',local_limit_label:'local'}];api.status[-1001].queue_depth=2;api.dispatches[-1001]=[{id:1,chat_id:-1001,request_id:'fixture-request',task_type:'chat',endpoint_id:'ep-main',model_ref:'fixture:tools',reason:'Fixture primary full -> ordered backup',status:'failed',error:'Fixture timeout trimmed',latency_ms:null,created_at:'2026-09-01T00:00:00Z'}];}
 await openAssistant(page);await tab(page,'模型与负载（高级）');if(state==='403'){await expect(page.getByText('负载状态暂时没有数据',{exact:true})).toBeVisible();await expect(page.getByText('调度记录暂时没有数据',{exact:true})).toBeVisible();}else if(state==='empty'){await expect(page.getByText('0',{exact:true}).first()).toBeVisible();await expect(page.getByText('未知',{exact:true}).first()).toBeVisible();await expect(page.getByText('还没有调度记录',{exact:true})).toBeVisible();}else{await expect(page.getByText('Fixture primary full -> ordered backup',{exact:true})).toBeVisible();await expect(page.getByText('冷却中',{exact:true})).toBeVisible();await expect(page.getByText('3 / 3',{exact:true})).toBeVisible();await expect(page.getByText('12 毫秒',{exact:true})).toHaveCount(0);await expect(page.getByText('2',{exact:true}).first()).toBeVisible();}
 expect(api.writes).toEqual([]);await expect(page.locator('input[type=password]')).toHaveCount(0);
});

test('runtime polling is aborted when leaving the advanced section',async({page,api})=>{
 await page.addInitScript(()=>{const original=window.fetch;(window as any).__aborts=[];window.fetch=(input,init)=>{init?.signal?.addEventListener('abort',()=>{(window as any).__aborts.push(String(input));});return original(input,init);};});await openAssistant(page);await page.clock.install();await tab(page,'模型与负载（高级）');const count=()=>api.requests.filter(r=>r.path.endsWith('/status')).length;const initial=count();await page.clock.fastForward(20010);await expect.poll(count).toBe(initial+1);await tab(page,'能查什么');await expect.poll(()=>page.evaluate(()=>[...new Set((window as any).__aborts)].sort())).toEqual(['/api/admin/groups/-1001/assistant/dispatches?limit=50','/api/admin/groups/-1001/assistant/status']);
});

test('skills are read only and keep the management surface in Chinese',async({page,api})=>{
 await openAssistant(page);await tab(page,'能查什么');await expect(page.getByText('只读',{exact:true})).toHaveCount(3);await expect(page.getByText('可写技能：无。当前群范围：已绑定。',{exact:true})).toBeVisible();await expect(page.getByRole('button',{name:/试运行|试跑|发送消息|执行工具/})).toHaveCount(0);await page.getByRole('button',{name:'去怎么说话设置'}).click();await expect(page.getByLabel('网页域名白名单')).toBeVisible();expect(api.writes).toEqual([]);
});
