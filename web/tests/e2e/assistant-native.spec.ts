import {test,expect,openAssistant,tab} from './assistant-fixtures';

test('legacy UI still shows old learning switch and can save settings',async({page,api})=>{
 await openAssistant(page);
 await tab(page,'回复与媒体');
 await expect(page.getByRole('switch',{name:'启用普通群聊自动学习'})).toBeVisible();
 await expect(page.getByTestId('native-group-settings')).toHaveCount(0);
 await expect(page.getByText('原生 SGB 助手',{exact:true})).toHaveCount(0);
});

test('native engine public UI read/write, permission surface and old memory freeze',async({page,api})=>{
 api.nativeEngine=true;
 await openAssistant(page);
 await expect(page.getByText('原生 SGB 助手',{exact:true})).toBeVisible();
 await tab(page,'回复与媒体');
 await expect(page.getByTestId('native-group-settings')).toBeVisible();
 await expect(page.getByRole('switch',{name:'启用普通群聊自动学习'})).toHaveCount(0);
 await page.getByRole('switch',{name:'原生仅@回复'}).click();
 await page.getByRole('button',{name:'保存原生群配置'}).click();
 await expect(page.getByText('原生群设置已保存；未覆盖实时生成的画像和调度回执')).toBeVisible();
 expect(api.writes.some(w=>w.path==='/api/admin/groups/-1001/assistant/native'&&w.method==='PUT'&&w.body.settings.at_reply_mode===true)).toBe(true);
 await tab(page,'群记忆');
 await expect(page.getByText('管理员永久记忆',{exact:true})).toBeVisible();
 await expect(page.getByText('旧助手档案（只读）',{exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'新增基础事实'})).toHaveCount(0);
 await page.getByLabel('新增管理员永久记忆').fill('管理员永久原文');
 await page.getByRole('button',{name:'添加永久记忆'}).click();
 await expect(page.getByText('#1 管理员永久原文')).toBeVisible();
 expect(api.writes.some(w=>w.path==='/api/admin/groups/-1001/assistant/native/memories'&&w.method==='POST'&&w.body.content==='管理员永久原文')).toBe(true);
 await tab(page,'能查什么');
 await expect(page.getByTestId('native-skills')).toBeVisible();
 await expect(page.getByTestId('native-skills').getByText(/memory_manage/)).toBeVisible();
 await expect(page.getByText('rule_manage和vote_ban不注册')).toBeVisible();
 await tab(page,'全局');
 await expect(page.getByTestId('native-global-settings')).toBeVisible();
 await page.getByLabel('热窗口消息数').fill('1800');
 await page.getByRole('button',{name:'保存原生全局配置'}).click();
 await expect(page.getByText('原生配置已保存并应用')).toBeVisible();
 expect(api.writes.some(w=>w.path==='/api/admin/assistant/native'&&w.method==='PUT'&&w.body.bot.memory_recent_messages===1800)).toBe(true);
});
