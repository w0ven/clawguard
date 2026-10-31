import { test, expect } from '@playwright/test';

const item = { id: 42, group_title: '测试群', action: 'mute', result: 'executed', explanation: '群规则处理', summary: '<script>unsafe</script>', created_at: '2026-10-31T06:00:00Z', appeal_status: null };
const receipt = { id: 7, case_id: 42, status: 'pending', reason: '这是我的理由', reference_url: '', created_at: '2026-10-31T06:05:00Z' };

test('record to verified appeal and receipt, preserving form on refresh', async ({page}) => {
 let challengeCount=0; let submitted: any;
 await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.continue() : route.abort());
 await page.addInitScript(() => { const w=window as any; w.turnstile={render: (_: unknown, o: any)=>{w.cf=o; return 'cf'},remove:()=>{}};w.hcaptcha={render:(_: unknown,o:any)=>{w.hc=o;return 'hc'},remove:()=>{}}; });
 await page.route('https://telegram.org/js/telegram-web-app.js*', r=>r.fulfill({contentType:'application/javascript',body:`window.Telegram={WebApp:{initData:'signed',ready(){},expand(){},BackButton:{onClick(){},offClick(){},show(){},hide(){}},safeAreaInset:{top:10,bottom:0},contentSafeAreaInset:{top:20,bottom:0}}}` }));
 await page.route('**/api/appeal/mini/**',r=>{
  const p=r.request().postDataJSON(); expect(p.init_data).toBe('signed');
  const url=r.request().url();
  if(url.endsWith('/list')) return r.fulfill({json:{cases:[item],has_more:false}});
  if(url.endsWith('/case')) return r.fulfill({json:{case:item,appeal:null}});
  if(url.endsWith('/challenge')){challengeCount++;return r.fulfill({json:{token:'challenge'+challengeCount,turnstile_sitekey:'cf',hcaptcha_sitekey:'hc',expires_in:600}})}
  submitted=p; return r.fulfill({json:{appeal:receipt}});
 });
 await page.goto('/appeal/mini#tgWebAppData=signed');
 await page.getByRole('button',{name:/CASE-42/}).click();
 await expect(page.getByText('<script>unsafe</script>')).toBeVisible();
 await page.getByLabel('申诉理由（必填）').fill('这是我的理由');
 await page.getByRole('button',{name:'下一步：安全验证'}).click();
 await expect.poll(()=>page.evaluate(()=>Boolean((window as any).cf && (window as any).hc))).toBe(true);
 await page.evaluate(()=>{(window as any).cf.callback('cf-token');(window as any).hc.callback('hc-token')});
 await page.getByRole('button',{name:'刷新验证'}).click();
 await expect.poll(()=>challengeCount).toBe(2);
 await expect.poll(()=>page.evaluate(()=>(window as any).cf?.cData)).toBe('challenge2');
 await page.evaluate(()=>{(window as any).cf.callback('cf-token2');(window as any).hc.callback('hc-token2')});
 await page.getByRole('button',{name:'提交申诉'}).click();
 await expect(page.getByText('申诉编号：APL-7')).toBeVisible();
 expect(submitted).toMatchObject({case_id:42,token:'challenge2',reason:'这是我的理由',cf_response:'cf-token2',h_response:'hc-token2'});
});

test('direct case link preserves Telegram fragment and security headers are scoped',async ({page,request})=>{
 await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.continue() : route.abort());
 await page.route('https://telegram.org/js/telegram-web-app.js*',r=>r.fulfill({contentType:'application/javascript',body:`window.Telegram={WebApp:{initData:'signed',ready(){},expand(){},BackButton:{onClick(){},offClick(){},show(){},hide(){}}}}`}));
 await page.route('**/api/appeal/mini/case',r=>r.fulfill({json:{case:{...item,action:'ban',result:'partial',explanation:'仅删除消息'},appeal:receipt}}));
 await page.goto('/appeal/mini?case=42#tgWebAppData=signed');
 await expect(page.getByText('申诉编号：APL-7')).toBeVisible();
 await expect(page.getByText('原拟处理：封禁 · 部分处理或状态待确认')).toBeVisible();
 await expect(page.getByText('说明：仅删除消息')).toBeVisible();
 expect(await page.evaluate(()=>location.search)).toBe(''); expect(await page.evaluate(()=>location.hash)).toContain('tgWebAppData');
 const mini=await request.get('/appeal/mini');expect(mini.headers()['referrer-policy']).toBe('no-referrer');expect(mini.headers()['content-security-policy']).toBe("frame-ancestors 'self' https://web.telegram.org");
 const home=await request.get('/');expect(home.headers()['content-security-policy']??'').not.toContain('web.telegram.org');
});

test('draft exit is confirmed and pending challenge cannot switch cases', async ({page}) => {
 await page.route('**/*', r=>new URL(r.request().url()).hostname==='127.0.0.1'?r.continue():r.abort());
 await page.route('https://telegram.org/js/telegram-web-app.js*',r=>r.fulfill({contentType:'application/javascript',body:`window.Telegram={WebApp:{initData:'signed',ready(){},expand(){},BackButton:{onClick(){},offClick(){},show(){},hide(){}}}}`}));
 let release!:()=>void; const pending=new Promise<void>(resolve=>{release=resolve}); let challenges=0;
 await page.route('**/api/appeal/mini/**',async r=>{const path=new URL(r.request().url()).pathname;if(path.endsWith('/list'))return r.fulfill({json:{cases:[item],has_more:false}});if(path.endsWith('/case'))return r.fulfill({json:{case:item,appeal:null}});if(path.endsWith('/challenge')){challenges++;await pending;return r.fulfill({status:503,json:{error:'暂不可用'}})}return r.abort()});
 await page.goto('/appeal/mini'); await page.getByRole('button',{name:/CASE-42/}).click(); await page.getByLabel('申诉理由（必填）').fill('草稿');
 page.once('dialog',d=>d.dismiss()); await page.getByRole('button',{name:'返回处理记录'}).click(); await expect(page.getByLabel('申诉理由（必填）')).toHaveValue('草稿');
 await page.getByRole('button',{name:'下一步：安全验证'}).click(); await expect(page.getByRole('button',{name:'返回处理记录'})).toBeDisabled(); expect(challenges).toBe(1); release(); await expect(page.locator('p[role="alert"]')).toContainText('暂不可用'); await expect(page.getByLabel('申诉理由（必填）')).toHaveValue('草稿');
});
