# Prompt 编辑器拆分实施计划

把目前"一个全局 `custom_rules` 同时管发言审核和简介审核"拆成两个独立槽位（`message_rules` / `bio_rules`），并升级 AI 测试页面与编辑器 UI。

> 本计划假设执行者熟悉项目结构。所有路径均相对于仓库根 `e:\DEV\clawguard`。

---

## 0. 必须保持不变的行为（不变量）

实施过程中以下功能必须保持现状，**不允许借机重构或修改**：

1. 群聊发言审核的判定逻辑、阈值、`actions_by_category` 映射、缓存策略、batch 行为、预算/单用户限额、CAS、关键词过滤、新人限制等——全部不动。
2. 入群验证 / 简介审核（关键词模式与 AI 模式）的触发时机、`profile_check_mode` 切换、bio 缓存（`bio_cache_ttl_minutes`）、白名单 `validVerdicts`（`ad/scam/spam/harass/porn/violence`）、阈值用 `policy.AI.Thresholds.Warn`、命中后的处置——不动。
3. 已存在群组配置中的 `ai.custom_rules` 不能因为这次改动失效或丢失，老群必须能继续按现有规则审核。
4. 数据库 schema 不动（没有新增列、没有 migration），所有变化通过 JSONB 配置字段完成。
5. 不修改任何与 AI 成本/预算相关的逻辑（用户明确表示不需要省钱考虑）。

---

## 1. 数据模型改动

### 1.1 `bot/internal/config/policy.go`

在 `AIPolicy` 结构体（约第 189 行）内追加两个字段，**保留** `CustomRules` 字段做兼容读取：

```go
type AIPolicy struct {
    // ... 现有字段全部保留 ...
    CustomRules  string `json:"custom_rules,omitempty"`  // 旧字段，保留做兼容；新代码不应再写入
    MessageRules string `json:"message_rules"`           // 新：群聊发言审核的自定义规则
    BioRules     string `json:"bio_rules"`               // 新：用户简介审核的自定义规则
    // ... 其余字段保留 ...
}
```

`DefaultPolicy.AI`（约第 297 行）：`MessageRules` 和 `BioRules` 默认空字符串即可，无需写明。

### 1.2 兼容迁移逻辑（关键）

在 `applyAIDefaults`（约第 529 行）末尾追加：

```go
// 兼容旧 custom_rules：仅当新字段为空、旧字段非空时回填，
// 避免把旧值同时写入两边后再次覆盖管理员的有意清空操作。
if strings.TrimSpace(policy.MessageRules) == "" && strings.TrimSpace(policy.CustomRules) != "" {
    policy.MessageRules = policy.CustomRules
}
if strings.TrimSpace(policy.BioRules) == "" && strings.TrimSpace(policy.CustomRules) != "" {
    policy.BioRules = policy.CustomRules
}
```

> 用户选择 1.B：旧 `custom_rules` 同时复制到两个新槽位。
> 此回填仅在读取时发生，不写回数据库；待管理员首次保存后由前端把 `custom_rules` 显式置空，完成持久迁移（详见 §3.2）。

---

## 2. 后端核心：AI 模块改动

### 2.1 `bot/internal/ai/moderator.go`

#### 2.1.1 拆分 base prompt

把现有的常量 `basePrompt`（第 23 行）替换为两个常量：

```go
const messageBasePrompt = `你是一个中文群聊反垃圾审核员。判断下面的消息属于哪类：
- clean（正常对话）
- ad（商业广告、引流、招聘、交友、币圈、刷单等）
- scam（诈骗）
- harass（骚扰辱骂）
- spam（无意义刷屏）
- suspicious（模糊不清但有风险）

输出 JSON：
{"items":[{"verdict":"...","confidence":0.0-1.0,"category":"招聘/交友/币圈/刷单/引流/政治/色情/正常","reason":"简短中文解释"}]}

管理员自定义规则（如无则忽略）：
%s

如果有图片，判断图片中的文字和画面内容（广告、二维码、色情、收款码、联系方式等）。

如果消息包含【跨聊天引用】块，表示用户从其他群/频道引用消息到本群。
广告号常用此方式引流色情/诈骗/赌博频道：自己只发空白或单字，让被引用的频道内容代为铺陈。
只要原消息来自陌生频道/bot 且涉及色情、赌博、诈骗、引流，即使本次消息本身无文字，也必须判定为 banned 或 suspicious。
【引用回复】【引用片段】同理，也要把被引用的内容纳入判断。
如果审核内容里有【链接预览】或【无法展开的 Telegram 链接】标记，说明用户消息里嵌了 Telegram 频道/消息链接。把链接预览的标题/描述视为用户本次发送的实际内容来判定，引流型内容判 ad，色情/赌博/诈骗按对应 verdict 判。

消息：
%s`

const bioBasePrompt = `你是一个中文 Telegram 用户资料简介审核员。判断下面的用户简介属于哪类：
- clean（正常简介）
- ad（商业广告、引流、招聘、交友、币圈、刷单等）
- scam（诈骗）
- spam（堆砌关键词、无意义刷屏式简介）
- harass（骚扰辱骂、攻击性内容）
- porn（色情引流）
- violence（暴力、血腥、极端内容）

输出 JSON：
{"items":[{"verdict":"...","confidence":0.0-1.0,"category":"招聘/交友/币圈/刷单/引流/政治/色情/暴力/正常","reason":"简短中文解释"}]}

管理员自定义规则（如无则忽略）：
%s

判定要点：
- 简介为空、或仅是普通自我介绍（职业、爱好、所在地、兴趣标签等）一律 clean。
- 留 Telegram/WhatsApp 链接、TG 频道/群组邀请、@用户名引流、加 vx/微信、TRC20/USDT/收款方式、境外博彩、刷单兼职——按对应 verdict 判，置信度通常 ≥ 0.7。
- verdict 与 category 不允许矛盾：clean 必须搭配"正常"，其他 verdict 不允许搭配"正常"。
- 没有"消息列表"概念，每次只判一条简介。

简介：
%s`
```

> 关键差异：bio 版本去掉了"图片""跨聊天引用""链接预览""消息列表"等仅适用于发言场景的指令；新增了与 `validVerdicts`（`ad/scam/spam/harass/porn/violence`）对齐的 verdict 集合，并写入"verdict 与 category 不允许矛盾"约束（针对最近 commit `a1f9e5c` 修复的同类问题做 prompt 端兜底）。

#### 2.1.2 `CheckInput` 增加场景字段

```go
type CheckInput struct {
    // ... 现有字段保留 ...
    Scene string // "message" 或 "bio"；空值视为 "message"（向后兼容）
}
```

新增辅助函数：

```go
func normalizeScene(s string) string {
    s = strings.TrimSpace(strings.ToLower(s))
    if s == "bio" {
        return "bio"
    }
    return "message"
}
```

#### 2.1.3 改写 `buildPrompt`

```go
func buildPrompt(scene string, customRules string, inputs []CheckInput) string {
    base := messageBasePrompt
    if normalizeScene(scene) == "bio" {
        base = bioBasePrompt
    }
    lines := make([]string, 0, len(inputs))
    for index, input := range inputs {
        msgLine := strings.TrimSpace(input.Text)
        if sn := strings.TrimSpace(input.SenderName); sn != "" {
            msgLine = fmt.Sprintf("[用户昵称: %s] %s", sn, msgLine)
        }
        lines = append(lines, fmt.Sprintf("%d. %s", index+1, msgLine))
    }
    return fmt.Sprintf(base, strings.TrimSpace(customRules), strings.Join(lines, "\n"))
}
```

并新增导出函数供 API 预览使用：

```go
// BuildPromptPreview 渲染指定场景的完整 prompt，用于管理后台预览。
// sampleText 为空时使用占位符。
func BuildPromptPreview(scene, customRules, sampleText string) string {
    if strings.TrimSpace(sampleText) == "" {
        sampleText = "<示例文本>"
    }
    return buildPrompt(scene, customRules, []CheckInput{{Text: sampleText}})
}
```

#### 2.1.4 `checkBatch` / `checkSingle` 选用对应规则

`checkBatch`（第 195 行）和 `checkSingle`（第 291 行）里把：
```go
prompt := buildPrompt(policy.CustomRules, ...)
```
改为：
```go
scene := normalizeScene(inputs[0].Scene)  // checkSingle 用 input.Scene
rules := policy.MessageRules
if scene == "bio" {
    rules = policy.BioRules
}
prompt := buildPrompt(scene, rules, ...)
```

#### 2.1.5 batch key 与 cache key 加 scene 维度

`CheckMessage`（第 148 行）：
```go
key := strconv.FormatInt(input.ChatID, 10) + ":" +
       strconv.FormatInt(input.UserID, 10) + ":" +
       normalizeScene(input.Scene)
```
> 防止同一用户的发言和 bio 调用错误地合批进同一个 LLM 请求（两者 prompt 不同，合批必崩）。

`cacheKey`（第 442 行）插入 scene 段：
```go
prefix := "ai:cache:" + strconv.FormatInt(input.ChatID, 10) + ":" + normalizeScene(input.Scene) + ":" + policyHash
```
然后接 `:image:...` 或 `:text:...`。
> 老缓存 key 不会被命中、自然过期即可，不需要清理。

`policyFingerprint`（第 451 行）改为接收 scene 参数，只哈希当前场景的 rules：
```go
func (m *Moderator) policyFingerprint(scene string, policy config.AIPolicy) string {
    rules := policy.MessageRules
    if normalizeScene(scene) == "bio" {
        rules = policy.BioRules
    }
    payload, err := json.Marshal(struct {
        Scene             string              `json:"scene"`
        Rules             string              `json:"rules"`
        PrimaryProvider   string              `json:"primary_provider"`
        // ... 其余字段保持原样 ...
    }{
        Scene: normalizeScene(scene),
        Rules: strings.TrimSpace(rules),
        // ...
    })
    // ... 其余实现不变 ...
}
```
调用方（cacheKey 里）传入 `input.Scene`。
> 这样改 bio 规则不会让发言缓存失效，反之亦然。

### 2.2 `bot/internal/bot/moderation.go`

三个 `CheckMessage` 调用点显式设置 `Scene`：

| 行号 | 调用上下文 | 设置 |
|---|---|---|
| 第 223 行 | 群聊发言审核（主路径） | `Scene: "message"` |
| 第 1764 行 | `checkProfile`（入群/验证时简介审核） | `Scene: "bio"` |
| 第 1866 行 | `checkProfileOnMessage`（未毕业用户发言前 bio 复查） | `Scene: "bio"` |

> 第 1762 行的 `text := "[新用户简介审核] 简介=" + ...` 与第 1865 行的 `text := "[未毕业用户发言前 bio 审核] 简介=" + bio` 这两个伪标签**仍然保留**。bio prompt 已经知道自己在判简介，伪标签只会被当作 bio 内容的一部分，无判定影响；保留可避免误改任何下游日志格式。

---

## 3. 后端 API 改动

### 3.1 `bot/internal/api/routes_admin.go` —— `handleAITest`（第 954 行）

请求 payload 扩展为：
```json
{
  "chat_id": 123 | null,
  "text": "测试文本",
  "scene": "message" | "bio",
  "model_ref": "newapi/glm-5" | null,
  "rules_override": "草稿规则文本" | null
}
```

实现要点：
1. `scene` 缺省按 `"message"` 处理。
2. 加载 policy 后构造 `testPolicy := policy.AI`，再按需覆盖：
   - `rules_override` 非空时：`scene == "bio"` → `testPolicy.BioRules = rules_override`，否则 `testPolicy.MessageRules = rules_override`。
   - `model_ref` 非空时：`testPolicy.PrimaryModelRef = model_ref`，并 `testPolicy.FallbackModelRefs = nil`、`testPolicy.PrimaryProvider = ""`、`testPolicy.PrimaryModel = ""`（避免老式 provider/model 字段污染解析）。
3. `BatchWindowMs = 1` 保留。
4. **强制 `SkipCache: true`**（新增）：测试调用必须每次都打到 LLM，不能命中缓存——否则修改规则后测试会读到旧结果，体验灾难。
5. 调用 `CheckMessage` 时 `Scene: scene` 透传。
6. 响应仍返回 `{"result": output}`，无需变。

### 3.2 新增端点 `handleAIPromptPreview`

路由：`POST /api/admin/ai-prompt-preview`，挂在与 `ai-test` 相同的管理员鉴权路由组下。

请求：
```json
{
  "scene": "message" | "bio",
  "rules_override": "..." | null,
  "sample_text": "示例输入" | null
}
```

实现：
1. 鉴权同 `ai-test`。
2. 加载全局 policy（`config.LoadPolicy(ctx, queries, 0)`），取 `MessageRules` 或 `BioRules` 作为默认 rules。
3. `rules_override` 非空时覆盖。
4. 调 `ai.BuildPromptPreview(scene, rules, sample_text)` 拿到完整 prompt 字符串。
5. 返回 `{"prompt": "...完整渲染后的 system prompt..."}`。

> 不调用任何 LLM，纯字符串拼接，零成本，可频繁点。

### 3.3 复用已有端点 `/api/admin/ai-providers`（第 940 行附近）

无需改动。前端调用它拿"模型下拉框"列表（每个 provider 下的 models）。

---

## 4. 前端改动

### 4.1 `web/app/prompt-editor/page.tsx` 整页重写

#### 布局（用户选择：上下两栏 + 测试区独立）

```
┌──────────────────────────────────────────────────────┬─────────────────────┐
│ 发言规则 [未保存徽章] [保存按钮]                      │ 实时测试            │
│ <textarea, min-height 240px>                          │ ┌─ 场景: ⦿发言 ○简介│
│                                                        │ │  模型: [下拉]    │
├──────────────────────────────────────────────────────┤ │  使用草稿规则 ☑  │
│ 简介规则 [未保存徽章] [保存按钮]                      │ │  <textarea>      │
│ <textarea, min-height 240px>                          │ │  [调用 AI 测试]  │
│                                                        │ │  [预览完整 prompt]│
│                                                        │ │  <pre 结果>       │
└──────────────────────────────────────────────────────┴─────────────────────┘
```

#### 状态字段

```ts
const [configState, setConfigState] = useState<Record<string, unknown>>({});
const [messageRules, setMessageRules] = useState("");
const [bioRules, setBioRules] = useState("");
const [originalMessage, setOriginalMessage] = useState("");
const [originalBio, setOriginalBio] = useState("");

const [testScene, setTestScene] = useState<"message" | "bio">("message");
const [testText, setTestText] = useState("");
const [testModel, setTestModel] = useState<string>("");      // 空 = 用配置默认
const [useDraftRules, setUseDraftRules] = useState(true);
const [models, setModels] = useState<Array<{ ref: string; label: string }>>([]);

const [result, setResult] = useState("");
const [previewOpen, setPreviewOpen] = useState(false);
const [previewText, setPreviewText] = useState("");
```

#### 加载阶段

`useEffect` 里：
1. `GET /api/admin/global-config` → 读 `config.ai.message_rules` 和 `config.ai.bio_rules`（**不再读 `custom_rules`**，因为后端 `applyAIDefaults` 已经做过兼容回填，前端拿到的两个字段已经是迁移后的值）。
2. `GET /api/admin/ai-providers` → 拍平成 `{ ref: "newapi/glm-5", label: "newapi · glm-5" }` 数组，写入 `models`。

#### 保存阶段

每个 textarea 独立"保存"按钮（不是一个全局保存），点击时：
```ts
await apiFetch("/api/admin/global-config", {
  method: "PUT",
  body: JSON.stringify({
    ...configState,
    ai: {
      ...((configState.ai as Record<string, unknown> | undefined) ?? {}),
      message_rules: messageRules,
      bio_rules: bioRules,
      custom_rules: "",  // 关键：首次保存即清空旧字段，完成持久迁移
    },
  }),
});
```

> 即使本次只改了一边，保存时仍然把两边都写进去 + `custom_rules: ""`，这样首次保存就完成迁移，后续 reload 不会再触发后端的兼容回填。

#### 测试调用

```ts
async function test() {
  const body: Record<string, unknown> = {
    text: testText,
    scene: testScene,
  };
  if (testModel) body.model_ref = testModel;
  if (useDraftRules) {
    body.rules_override = testScene === "bio" ? bioRules : messageRules;
  }
  const p = await apiFetch<{ result: unknown }>("/api/admin/ai-test", {
    method: "POST",
    body: JSON.stringify(body),
  });
  setResult(JSON.stringify(p.result, null, 2));
}
```

#### Prompt 预览

按钮 `预览完整 prompt`：
```ts
async function preview() {
  const body: Record<string, unknown> = {
    scene: testScene,
    sample_text: testText || undefined,
  };
  if (useDraftRules) {
    body.rules_override = testScene === "bio" ? bioRules : messageRules;
  }
  const p = await apiFetch<{ prompt: string }>("/api/admin/ai-prompt-preview", {
    method: "POST",
    body: JSON.stringify(body),
  });
  setPreviewText(p.prompt);
  setPreviewOpen(true);
}
```

弹出一个简单 modal 或就近展开一个折叠面板，里面 `<pre>` 显示 `previewText`，可以"复制全文"。

#### 占位文案建议

- 发言规则 placeholder：`例如：本群禁止讨论币圈、刷单、招聘；本群禁止所有外站链接；娱乐内容从宽处理。`
- 简介规则 placeholder：`例如：简介里出现 TRC20/USDT/收款码一律视为引流；简介带博彩相关词从严判定 ad。`

---

## 5. 完成后的行为对照

| 场景 | 现状（拆分前） | 拆分后 |
|---|---|---|
| 群聊发言 AI 审核 | 读 `policy.AI.CustomRules`，拼 messageBasePrompt（实际是同一个 basePrompt） | 读 `policy.AI.MessageRules`，拼 `messageBasePrompt` |
| 入群验证 / 简介 AI 审核 | 读 `policy.AI.CustomRules`，拼同上的发言版 prompt | 读 `policy.AI.BioRules`，拼 `bioBasePrompt` |
| 未毕业用户发言前 bio 复查 | 同上 | 同上 |
| 老群（DB 里只有 `custom_rules`，没有新字段） | —— | 后端读取时自动把 `custom_rules` 回填到 `MessageRules` 和 `BioRules`，行为完全等价于拆分前 |
| 管理员首次在新编辑器点保存 | —— | 前端写入新字段 + `custom_rules: ""`，持久迁移完成 |
| AI 测试页面 | 只能测全局配置 + 当前已保存的规则 | 可选场景、可临时换模型、默认用草稿规则 |
| Prompt 预览 | 无 | 新按钮，零成本随时看完整 system prompt |

---

## 6. 验收 checklist（codex 实施完后必须人工跑一遍）

### 6.1 兼容性（最重要，验证不变量）

- [ ] 找一个**已有 `custom_rules` 但没有新字段**的群（或在 DB 里造一个），不打开新编辑器，直接在群里发一条违规消息，确认审核结果跟拆分前一致。
- [ ] 同一个群，触发一次入群简介审核（或调一次 `checkProfileOnMessage`），确认 bio 命中行为跟拆分前一致。
- [ ] 重启 bot 进程不会因新字段缺失而 panic 或报 unmarshal 错误。

### 6.2 拆分生效

- [ ] 在新编辑器把 `message_rules` 改成"禁止任何提到香蕉的发言"，bio_rules 改成"禁止任何提到苹果的简介"。保存。
- [ ] 群里发"我喜欢香蕉" → 命中（违规）；"我喜欢苹果" → 不命中。
- [ ] 改一个用户的 bio 为"我卖苹果" → 触发 bio 审核命中；改成"我卖香蕉" → 不命中。
- [ ] **结论：两个槽位互不干扰。**

### 6.3 持久迁移

- [ ] 第一次在新编辑器点保存后，去 DB 查 `global_config.config.ai`，确认 `custom_rules` 字段已变为空字符串，`message_rules` 和 `bio_rules` 字段写入了正确值。
- [ ] 把新字段 message_rules 手动改回空字符串、custom_rules 改回非空，重启读取——验证后端兼容回填仍然工作（应能复活到 message_rules）。
- [ ] 在新编辑器把 message_rules 故意清空保存——重新加载后仍然是空（不会被旧 custom_rules 复活），证明前端的 `custom_rules: ""` 写入有效。

### 6.4 AI 测试页面

- [ ] 切换"测发言/测简介"两种场景，分别测试，结果合理。
- [ ] 模型下拉能列出后台所有已配置的模型；选一个非默认模型，测试结果里 `output.result.model` 字段确实是所选模型。
- [ ] "使用草稿规则"打开时，改了文本框还没点保存，测试结果应该按草稿规则判；关掉时按已保存规则判。
- [ ] 同一个测试文本反复点"调用 AI 测试"，每次都应该真的调 LLM（不会被缓存）。

### 6.5 Prompt 预览

- [ ] "测发言"场景下点预览，看到的 prompt 包含 `messageBasePrompt` 全文 + 当前 message_rules。
- [ ] "测简介"场景下点预览，看到的 prompt 包含 `bioBasePrompt` 全文 + 当前 bio_rules，且**不包含**"图片""跨聊天引用""链接预览""消息列表"这些发言版独有的指令。
- [ ] 预览不调 LLM，无费用，可疯狂点。

### 6.6 Cache 隔离

- [ ] 在群里发一条相同文本两次（间隔超 batch window），第二次走缓存（看 redis `ai:cache:hit` 计数 +1）。
- [ ] 改 message_rules 后再发同一条文本，应该 cache miss、重新调 LLM。
- [ ] 只改 bio_rules、不改 message_rules，再发同一条群消息，应该仍然 cache hit（证明两个槽位的缓存独立）。

---

## 7. 不在本计划范围内的事

明确不做，避免越界：

- 不新增 DB 列、不写 SQL migration。
- 不调整 `actions_by_category`、阈值、单用户限额、预算、bio 缓存 TTL 等任何已有配置。
- 不为 bio 单独配模型/fallback chain（用户明确不考虑成本）。
- 不改变 bio 调用点的 `SkipCache` / `BatchWindowMs` 等参数。
- 不删除 `AIPolicy.CustomRules` 字段（保留做永久兼容；如未来要清理，单开 PR）。
- 不动 `web/components`、`web/lib`、`web/middleware.ts` 等编辑器页面之外的前端代码，除非确实需要新增 API client helper。

---

## 8. 文件改动清单（速查）

| 文件 | 改动类型 |
|---|---|
| `bot/internal/config/policy.go` | 加 2 个字段 + 兼容回填 |
| `bot/internal/ai/moderator.go` | 拆 base prompt、加 Scene、改 buildPrompt、加 BuildPromptPreview、改 cacheKey/policyFingerprint/batch key |
| `bot/internal/bot/moderation.go` | 3 个 `CheckMessage` 调用点显式加 `Scene` 字段 |
| `bot/internal/api/routes_admin.go` | 改 `handleAITest`、新增 `handleAIPromptPreview` 及其路由注册 |
| `web/app/prompt-editor/page.tsx` | 整页重写为双槽位 + 增强测试 + 预览 |

预计代码改动量：后端 ~150 行（含 prompt 文案），前端 ~250 行。
