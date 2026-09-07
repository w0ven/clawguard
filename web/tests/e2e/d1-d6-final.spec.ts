// Final bounded regression additions; uses existing non-production API/Telegram fixtures.
import {test,expect,login,openGroup,draftGroup,field,theme,dialogs,mini,group} from './ui-fixtures';

for(const action of ['back','miniBack'])test(`D1 ${action} repeated cancel then accept and forward`,async({page},info)=>{
 if(action==='miniBack')await mini(page);
 await login(page);await page.goto('/groups');await page.getByRole('link',{name:group.title,exact:true}).click();
 const input=await draftGroup(page);let accept=false;const messages:string[]=[];page.on('dialog',async d=>{messages.push(d.message());accept?await d.accept():await d.dismiss()});
 const back=async()=>{if(action==='back')await page.goBack();else await page.evaluate(()=>(window as any).__miniBack())};
 for(let n=1;n<=2;n++){await back();await page.waitForTimeout(1200);await expect(page).toHaveURL(/\/groups\/-1001$/);await expect(page.getByRole('heading',{name:group.title})).toBeVisible();await expect(input).toHaveValue('321');expect(messages).toHaveLength(n);}
 accept=true;await back();await expect(page).toHaveURL(/\/groups$/);await expect(page.getByRole('heading',{name:'群管理',exact:true})).toBeVisible();expect(messages).toHaveLength(3);
 await page.goForward();await expect(page).toHaveURL(/\/groups\/-1001$/);await expect(page.getByRole('heading',{name:group.title})).toBeVisible();expect(messages).toHaveLength(3);
 await info.attach('D1-cancel-accept-forward',{body:JSON.stringify({messages,url:page.url(),heading:await page.locator('h1').innerText()}),contentType:'application/json'});
});

test('D3 response retains in-flight 1900 as dirty and second payload',async({page,mock},info)=>{
 await login(page);await page.goto('/llm');await page.getByRole('button',{name:'AdKiller',exact:true}).click();
 const input=page.getByRole('spinbutton',{name:'超时（ms）'});await input.fill('1700');mock.delay=1800;
 await page.getByRole('button',{name:'保存 AdKiller 设置',exact:true}).click();await expect.poll(()=>mock.writes.length).toBe(1);
 const saving=page.getByRole('button',{name:'保存中…',exact:true});await expect(saving).toBeDisabled();await expect(page.getByRole('button',{name:'增加分段',exact:true})).toBeDisabled();
 // aria-disabled is a saving semantic, not a native lock on the timeout input.
 expect(await input.evaluate(el=>(el as HTMLInputElement).disabled)).toBe(false);
 await input.focus();await page.keyboard.press('ControlOrMeta+A');await page.keyboard.insertText('1900');await expect(input).toHaveValue('1900');await expect(saving).toBeVisible();
 const during={value:await input.inputValue(),saving:await saving.isVisible(),writes:JSON.parse(JSON.stringify(mock.writes))};
 await expect(page.getByText('AdKiller 设置已保存',{exact:true})).toBeVisible();await expect(input).toHaveValue('1900');
 const messages=dialogs(page);await page.getByRole('button',{name:/^Models/}).click();expect(messages).toHaveLength(1);await expect(input).toHaveValue('1900');await expect(page).toHaveURL(/\/llm$/);
 mock.delay=0;await page.getByRole('button',{name:'保存 AdKiller 设置',exact:true}).click();await expect.poll(()=>mock.writes.length).toBe(2);
 expect(mock.writes.map(w=>({path:w.path,section:w.body.section,timeout:w.body.config.ai.adkiller.timeout_ms}))).toEqual([{path:'/api/admin/global-config',section:'adkiller',timeout:1700},{path:'/api/admin/global-config',section:'adkiller',timeout:1900}]);
 await expect(input).toHaveAttribute('aria-disabled','false');await page.getByRole('button',{name:/^Models/}).click();expect(messages).toHaveLength(1);await expect(input).toHaveCount(0);
 await info.attach('D3-two-snapshots-and-dirty',{body:JSON.stringify({during,writes:mock.writes,messages}),contentType:'application/json'});
});

for(const value of ['emerald','ocean','graphite'])test(`D4 Mini ${value} safe-area save and navigation real clicks`,async({page,mock},info)=>{
 await mini(page);await openGroup(page);await draftGroup(page);
 await page.evaluate(()=>document.documentElement.style.setProperty('--tg-safe-area-inset-bottom','34px'));
 if(info.project.name==='mobile'){await page.getByRole('button',{name:'全部',exact:true}).click();await theme(page,value);await page.getByRole('button',{name:'关闭',exact:true}).click();}else await theme(page,value);
 const save=page.getByRole('button',{name:'保存策略',exact:true});
 const geometry=await save.evaluate(el=>{const r=el.getBoundingClientRect(),n=document.querySelector('.miniapp-bottom-nav')?.getBoundingClientRect(),bar=el.closest('.miniapp-savebar')!.getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return {rect:{x:r.x,y:r.y,width:r.width,height:r.height,bottom:r.bottom},barBottom:bar.bottom,navTop:n?.top,hit:el===hit||el.contains(hit),safe:getComputedStyle(document.documentElement).getPropertyValue('--tg-safe-area-inset-bottom')}});
 expect(geometry.hit).toBe(true);expect(geometry.rect.y).toBeGreaterThanOrEqual(0);expect(geometry.rect.bottom).toBeLessThanOrEqual(page.viewportSize()!.height);
 if(info.project.name==='mobile')expect(geometry.navTop!-geometry.barBottom).toBeGreaterThanOrEqual(11);
 await save.click();await expect.poll(()=>mock.writes.length).toBe(1);expect(mock.writes[0].body.verify.timeout_seconds).toBe(321);await expect(page.getByRole('button',{name:'已保存',exact:true})).toBeDisabled();
 if(info.project.name==='mobile'){await page.getByRole('button',{name:'全部',exact:true}).click();await expect(page.getByRole('heading',{name:'全部功能',exact:true})).toBeVisible();await page.getByRole('button',{name:'关闭',exact:true}).click();}
 await info.attach('D4-safearea-click',{body:JSON.stringify({geometry,writes:mock.writes}),contentType:'application/json'});
});

for(const existing of [false,true])test(`D5 ${existing?'edit':'create'} Esc cancel accept focus and tab loop`,async({page,mock},info)=>{
 if(existing)await page.route('**/api/admin/groups/-1001/scheduled-messages',route=>route.fulfill({json:{scheduled_messages:[{id:9,chat_id:-1001,name:'Fixture existing',schedule_type:'interval',interval_minutes:60,daily_times:[],timezone:'Asia/Shanghai',content:'existing content',buttons:[],auto_delete_seconds:0,enabled:true,status:'active',last_run_at:null,last_message_id:null,last_error:null,last_skip_reason:null,next_run_at:null,created_at:'2026-09-01T00:00:00Z',updated_at:'2026-09-01T00:00:00Z'}],limit:20}}));
 await openGroup(page);await page.getByRole('button',{name:'群运营',exact:true}).click();await page.getByRole('button',{name:'定时消息',exact:true}).click();
 const trigger=page.getByRole('button',{name:existing?'编辑':'新建定时消息',exact:true});await trigger.click();
 const dialog=page.getByRole('dialog',{name:existing?'编辑定时消息':'新建定时消息',exact:true}),input=dialog.getByPlaceholder('例如：每日群公告');
 await expect(dialog).toHaveAttribute('aria-modal','true');await expect(input).toBeFocused();
 const close=dialog.getByRole('button',{name:'关闭',exact:true}),save=dialog.getByRole('button',{name:'保存',exact:true});await close.focus();await page.keyboard.press('Shift+Tab');await expect(save).toBeFocused();await page.keyboard.press('Tab');await expect(close).toBeFocused();
 await input.fill('Esc retained name');await dialog.locator('textarea').fill('Esc retained body');await input.focus();
 let accept=false;const messages:string[]=[];page.on('dialog',async d=>{messages.push(d.message());accept?await d.accept():await d.dismiss()});
 await page.keyboard.press('Escape');expect(messages).toHaveLength(1);await expect(dialog).toBeVisible();await expect(input).toHaveValue('Esc retained name');await expect(dialog.locator('textarea')).toHaveValue('Esc retained body');await expect(input).toBeFocused();await expect(page).toHaveURL(/\/groups\/-1001$/);
 accept=true;await page.keyboard.press('Escape');expect(messages).toHaveLength(2);await expect(dialog).toHaveCount(0);await expect(trigger).toBeFocused();expect(mock.writes).toEqual([]);
 await info.attach('D5-Esc-branches',{body:JSON.stringify({existing,messages,focusRestored:await trigger.evaluate(el=>el===document.activeElement)}),contentType:'application/json'});
});

test('D5 saving Escape cannot close and failure keeps focused draft',async({page,mock})=>{
 await openGroup(page);await page.getByRole('button',{name:'群运营',exact:true}).click();await page.getByRole('button',{name:'定时消息',exact:true}).click();const trigger=page.getByRole('button',{name:'新建定时消息',exact:true});await trigger.click();const dialog=page.getByRole('dialog');await dialog.getByPlaceholder('例如：每日群公告').fill('pending');await dialog.locator('textarea').fill('pending content');mock.delay=1500;mock.status=500;await dialog.getByRole('button',{name:'保存',exact:true}).click();const messages=dialogs(page);await expect(dialog.getByPlaceholder('例如：每日群公告')).toBeDisabled();await page.keyboard.press('Escape');await expect(dialog).toBeVisible();expect(messages).toHaveLength(0);await expect(page.getByText('Fixture 500 rejected',{exact:true})).toBeVisible();await expect(dialog.getByPlaceholder('例如：每日群公告')).toHaveValue('pending');await page.keyboard.press('Escape');expect(messages).toHaveLength(1);await expect(dialog).toBeVisible();page.removeAllListeners('dialog');dialogs(page,true);await page.keyboard.press('Escape');await expect(trigger).toBeFocused();
});

test('D6 setItem failure keeps selected in-memory theme and draft',async({page})=>{
 await page.addInitScript(()=>{const original=Storage.prototype.setItem;Storage.prototype.setItem=function(key,value){if(this===localStorage&&key==='clawguard-ui-theme')throw new Error('fixture setItem blocked');return original.call(this,key,value)}});await openGroup(page);const input=await draftGroup(page);await theme(page,'ocean');await expect(input).toHaveValue('321');await expect(page.getByRole('combobox',{name:'界面主题'}).filter({visible:true}).first()).toHaveValue('ocean');
});
