import { test, expect, login, openGroup, draftGroup, field, group, policy } from './ui-fixtures';

const revA='a'.repeat(64), revB='b'.repeat(64);

test('group CAS advances acknowledged revision and missing success revision keeps draft', async ({page,mock})=>{
 await openGroup(page);const input=await draftGroup(page);await page.getByRole('button',{name:'保存策略',exact:true}).click();await expect(page.getByRole('button',{name:'已保存',exact:true})).toBeDisabled();expect(mock.writes[0].body.expected_config_revision).toBe(revA);
 await input.fill('322');await page.getByRole('button',{name:'保存策略',exact:true}).click();await expect.poll(()=>mock.writes.length).toBe(2);expect(mock.writes[1].body.expected_config_revision).toBe(revB);await expect(page.getByRole('button',{name:'已保存',exact:true})).toBeDisabled();
 await page.route('**/api/admin/groups/-1001/config',r=>r.fulfill({json:{group,merged_policy:policy}}));await input.fill('323');await page.getByRole('button',{name:'保存策略',exact:true}).click();await expect(page.getByText(/保存结果缺少群配置版本/)).toBeVisible();await expect(input).toHaveValue('323');await expect(page.getByRole('button',{name:'保存策略',exact:true})).toBeDisabled();
});

test('scoped model picker uses sanitized API, preserves unavailable model refs on unrelated save',async({page,mock})=>{
 await login(page);
 await page.route('**/api/admin/groups/-1001',r=>r.fulfill({json:{group:{...group,config:{...group.config,ai:{primary_model_ref:'retired:old',fallback_model_refs:['retired:fallback']}}},merged_policy:policy,config_revision:revA}}));
 let picker=0;await page.route('**/api/admin/ai-models',r=>{picker++;return r.fulfill({json:{providers:[{name:'new',label:'New',models:[{key:'safe',label:'Safe'}]}]}})});
 await page.route('**/api/admin/llm/models',r=>r.fulfill({status:403,json:{error:'owner only'}}));
 await page.goto('/groups/-1001');await expect(page.getByRole('heading',{name:group.title})).toBeVisible();await page.getByRole('button',{name:'AI 与反馈',exact:true}).click();
 const card=field(page,'主模型（新）');const select=card.locator('select');await expect(select).toHaveValue('retired:old');await expect(select.locator('option[value="new:safe"]')).toHaveText('new:safe · Safe');await expect(select.locator('option[value="retired:old"]')).toContainText('当前不可用');expect(picker).toBeGreaterThan(0);
 await page.getByRole('button',{name:'入群与资料',exact:true}).click();await draftGroup(page);await page.getByRole('button',{name:'保存策略',exact:true}).click();await expect.poll(()=>mock.writes.length).toBe(1);expect(mock.writes[0].body.config.ai).toEqual({primary_model_ref:'retired:old',fallback_model_refs:['retired:fallback']});
});

for(const status of [200,409])test(`LLM section version ${status}`,async({page,mock})=>{
 await login(page);await page.goto('/llm');await page.getByRole('button',{name:'探活设置',exact:true}).click();const input=page.getByRole('spinbutton',{name:/默认探活间隔/});await expect(input).toHaveValue('120');await input.fill('180');mock.status=status;await page.getByRole('button',{name:'保存设置',exact:true}).click();await expect.poll(()=>mock.writes.length).toBe(1);expect(mock.writes[0].body).toEqual({section:'llm',expected_version:7,config:{ai:{probe_enabled:false,auto_degrade:true,probe_interval_seconds:180}}});if(status===409){await expect(page.getByText(/配置已被其他人更新/)).toBeVisible();await expect(input).toHaveValue('180')}else await expect(page.getByText('探活设置已保存')).toBeVisible();
});

test('dashboard controls disabled until actual state has loaded',async({page})=>{
 await login(page);let release!:()=>void;const pending=new Promise<void>(r=>release=r);await page.route('**/api/admin/system-state',async r=>{await pending;return r.fulfill({json:{state:{ai_paused:true,actions_paused:false,frozen:false}}})});await page.goto('/dashboard');const switches=page.getByRole('switch');await expect(switches.first()).toBeDisabled();release();await expect(switches.first()).toBeEnabled();await expect(switches.first()).toHaveAttribute('aria-checked','true');
});

for(const kind of ['violations','ai-review'] as const)test(`${kind} ignores delayed obsolete filter results`,async({page})=>{
 await login(page);let release!:()=>void;const pending=new Promise<void>(r=>release=r);let oldStarted=0;
 const endpoint=kind==='violations'?'violations':'ai-decisions';
 await page.route(`**/api/admin/${endpoint}**`,async r=>{const user=new URL(r.request().url()).searchParams.get('user_id');if(user==='111'){oldStarted++;await pending}const row=kind==='violations'?{id:1,chat_id:-1001,user_id:Number(user||0),rule:'fixture',action:'ban',created_at:'2026-10-31T06:00:00Z'}:{id:1,chat_id:-1001,user_id:Number(user||0),verdict:'normal',action:'none',created_at:'2026-10-31T06:00:00Z'};return r.fulfill({json:{[kind==='violations'?'violations':'decisions']:user?[row]:[]}})});
 if(kind==='violations')await page.route('**/api/admin/warnings**',r=>r.fulfill({json:{warnings:[]}}));
 await page.goto(`/${kind}`);const filter=page.getByPlaceholder('user_id');await filter.fill('111');await expect.poll(()=>oldStarted).toBeGreaterThan(0);await filter.fill('222');const newer=page.getByText(kind==='violations'?'222':'用户 222',{exact:true}).first();const older=page.getByText(kind==='violations'?'111':'用户 111',{exact:true});await expect(newer).toBeVisible();release();await page.waitForTimeout(300);await expect(newer).toBeVisible();await expect(older).toHaveCount(0);
});

test('trust ignores delayed results from previous applied filter',async({page})=>{
 await login(page);let release!:()=>void;const pending=new Promise<void>(r=>release=r);let started=0;
 await page.route('**/api/admin/user-trust**',async r=>{const user=new URL(r.request().url()).searchParams.get('user_id');if(user==='111'){started++;await pending}const item={chat_id:-1001,user_id:Number(user||0),username:`fixture_${user}`,status:'trusted',score:0.8,messages_checked:0,messages_clean:0,joined_at:'2026-10-28T00:00:00Z',updated_at:'2026-10-28T00:00:00Z'};return r.fulfill({json:{items:user?[item]:[],total:user?1:0,has_more:false,counts:{}}})});
 await page.goto('/trust');const filter=page.getByPlaceholder('按 user_id 精确搜索');await filter.fill('111');await page.getByRole('button',{name:'筛选',exact:true}).click();await expect.poll(()=>started).toBeGreaterThan(0);await filter.fill('222');await page.getByRole('button',{name:'筛选',exact:true}).click();await expect(page.getByText('@fixture_222')).toBeVisible();release();await page.waitForTimeout(300);await expect(page.getByText('@fixture_222')).toBeVisible();await expect(page.getByText('@fixture_111')).toHaveCount(0);
});

test('audit ignores late response after newer refresh',async({page})=>{
 await login(page);let release!:()=>void;const pending=new Promise<void>(r=>release=r);let started=0;
 await page.route('**/api/admin/audit**',async r=>{const chat=new URL(r.request().url()).searchParams.get('chat_id');if(chat==='111'){started++;await pending}const audit=chat?[{id:Number(chat),scope:'fixture',action:`action_${chat}`,chat_id:Number(chat),admin_id:1,created_at:'2026-10-31T06:00:00Z'}]:[];return r.fulfill({json:{audit}})});
 await page.goto('/audit');const filter=page.getByPlaceholder('按 chat_id 过滤');await filter.fill('111');await page.getByRole('button',{name:'刷新',exact:true}).click();await expect.poll(()=>started).toBeGreaterThan(0);await filter.fill('222');await page.getByRole('button',{name:'刷新',exact:true}).click();await expect(page.getByText('action_222',{exact:true})).toBeVisible();release();await page.waitForTimeout(300);await expect(page.getByText('action_222',{exact:true})).toBeVisible();await expect(page.getByText('action_111',{exact:true})).toHaveCount(0);
});

test('profile and message policy help explains existing independent semantics',async({page})=>{
 await login(page);await page.goto('/prompt-editor');await expect(page.getByText(/简介危险判定达到 Warn 阈值后直接封禁/)).toBeVisible();await expect(page.getByText(/规则中写 ban 不会覆盖消息分类动作策略/)).toBeVisible();
 await page.goto('/groups/global-config');await page.getByRole('button',{name:'AI 与反馈',exact:true}).click();await expect(page.getByText(/简介危险判定达到此阈值后直接封禁/)).toBeVisible();
});

test('AdKiller section replacement retains legacy and unedited fields',async({page,mock})=>{
 await login(page);await page.route('**/api/admin/global-config',r=>r.request().method()==='GET'?r.fulfill({json:{version:7,config:{ai:{adkiller:{enabled:true,min_score:94,timeout_ms:1500,on_failure:'fallback',enabled_chat_ids:[],skip_followup:true,fixture_unknown:{keep:'yes'},score_bands:[{min_score:0,max_score:100,action:'none',fixture_band:'retain'}]}}}}}):r.fallback());
 await page.goto('/llm');await page.getByRole('button',{name:'AdKiller',exact:true}).click();const timeout=page.getByRole('spinbutton',{name:'超时（ms）'});await expect(timeout).toHaveValue('1500');await timeout.fill('1700');await page.getByRole('button',{name:'保存 AdKiller 设置',exact:true}).click();await expect.poll(()=>mock.writes.length).toBe(1);expect(mock.writes[0].body.expected_version).toBe(7);expect(mock.writes[0].body.config.ai.adkiller).toMatchObject({min_score:94,skip_followup:true,fixture_unknown:{keep:'yes'},score_bands:[{min_score:0,max_score:100,action:'none',fixture_band:'retain'}],timeout_ms:1700});
});

test('live event filter clears old results while new response is pending',async({page})=>{
 await login(page);let release!:()=>void;const pending=new Promise<void>(r=>release=r);let started=0;
 await page.route('**/api/admin/events**',async r=>{const type=new URL(r.request().url()).searchParams.get('type');if(type==='ai_decision'){started++;await pending}return r.fulfill({json:{events:[{id:1,type:type==='ai_decision'?'ai_decision':'violation',created_at:'2026-10-31T06:00:00Z',chat_id:-1001,user_id:123,title:type==='ai_decision'?'新结果':'旧结果'}],total:1,has_more:false}})});
 await page.goto('/dashboard');await expect(page.getByText('旧结果',{exact:true})).toBeVisible();await page.locator('select').filter({has:page.getByRole('option',{name:'AI 判定'})}).selectOption('ai_decision');await expect.poll(()=>started).toBeGreaterThan(0);await expect(page.getByText('旧结果',{exact:true})).toHaveCount(0);release();await expect(page.getByText('新结果',{exact:true})).toBeVisible();
});
