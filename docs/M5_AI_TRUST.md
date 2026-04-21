# M5 设计：AI 新人识别 + 信任系统

## 目标
解决"潜伏广告号"——加群后观察 N 天再发广告的账号，传统关键词/链接规则无效。

方案：**给用户引入信任状态机（new → watch → trusted）**，新人期内所有消息过 AI 审核；同时通过 Web 面板让可乐自由调节 AI 行为。

---

## 1. 信任状态机

```
  入群 → 验证通过
            │
            ▼
       ┌─────────┐
       │   new   │ ← 新人观察期（默认前 10 条 or 入群 7 天）
       └─────────┘
            │
            ├─ 发消息被 AI 判定为广告 ───→ suspicious / banned
            │
            ├─ 达到毕业条件 ───────────→ trusted（老成员，跳过 AI）
            │
            └─ 管理员手动标记 ─────────→ trusted / banned
```

### user_trust 表
```sql
CREATE TABLE user_trust (
    chat_id BIGINT,
    user_id BIGINT,
    joined_at TIMESTAMPTZ,
    status TEXT DEFAULT 'new',     -- new | trusted | suspicious | banned
    score FLOAT DEFAULT 0.5,       -- 0-1 可信度（越高越可信）
    messages_checked INT DEFAULT 0,-- AI 审核过的消息数
    messages_clean INT DEFAULT 0,  -- AI 判为安全的消息数
    graduated_at TIMESTAMPTZ,
    notes TEXT,
    PRIMARY KEY (chat_id, user_id)
);

CREATE TABLE ai_decisions (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT,
    user_id BIGINT,
    message_id BIGINT,
    message_text TEXT,
    model TEXT,
    prompt_version TEXT,
    verdict TEXT,                  -- clean | ad | scam | harass | spam | suspicious
    confidence FLOAT,              -- 0-1
    category TEXT,                 -- 招聘/交友/币圈/刷单/引流/正常
    reason TEXT,
    action_taken TEXT,             -- none | flag | delete | warn | mute | ban
    admin_override TEXT,           -- null | confirm | false_positive | false_negative
    latency_ms INT,
    cost_cents FLOAT,              -- 本次调用估算成本（分）
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_ai_decisions_user ON ai_decisions(chat_id, user_id, created_at DESC);
CREATE INDEX idx_ai_decisions_pending ON ai_decisions(admin_override) WHERE admin_override IS NULL;
```

### 毕业条件（任一满足即 graduated）
- `messages_clean >= policy.AI.GraduateAfterMessages`（默认 10）
- `NOW() - joined_at >= policy.AI.GraduateAfterDays`（默认 7）
- 管理员手动标记

### 特殊状态
- **suspicious**: AI 判为中等置信度广告，只标记、不动手，等管理员审
- **banned**: 直接踢出 + 写 CAS 候选（本地黑名单）

---

## 2. AI 审核流程

```
新人发消息
    ▼
查 user_trust.status
    │
    ├─ trusted → 走常规 filter → 过
    ├─ banned  → 删 + 踢 → 结束
    └─ new     → 送 AI
                   │
                   ▼
           HTTP 调 LLM（model 由 policy 指定）
                   │
                   ▼
           解析 JSON：{verdict, confidence, category, reason}
                   │
                   ▼
           按 policy.AI.Thresholds 决策：
           - confidence ≥ ban_threshold (0.9)    → delete + ban + 写 decisions
           - confidence ≥ mute_threshold (0.75)  → delete + mute + 写 decisions
           - confidence ≥ warn_threshold (0.5)   → warn + 写 decisions
           - confidence ≥ flag_threshold (0.3)   → 仅标记 suspicious
           - < flag_threshold                   → 放行，messages_clean++
                   │
                   ▼
           写 ai_decisions 表，供 Web 面板 review
```

### AI 调用优化
1. **异步并发**：消息先入内存队列，worker 池并发调 AI（不阻塞 webhook 响应）
2. **批量合并**：同一秒内多条新人消息合批（一次 prompt 发多条，省 token）
3. **短消息跳过**：< 5 字符消息直接放行（"好的"、"谢谢"）
4. **纯 emoji / 纯图片**：可选走视觉模型，默认跳过
5. **缓存**：相同消息哈希 24h 内复用判决（广告号常复制粘贴）

---

## 3. Web 面板可配置项（AI 相关）

### 3.1 模型选择（Model Pool）
支持**多模型切换 + fallback 链**。面板下拉框：

**OpenClaw 已内置 alias**：
- 国产快速：`TencentGLM5`、`MiMoPro`、`GLM-5-Turbo`、`TencentMiniMax25`、`TencentKimi25`
- 国产强力：`GLM-5.1`、`HunyuanTurboS`、`Hunyuan2Thinking`、`DoubaoSeed 2.0 Pro`
- 国际：`CCSonnet46`、`CCOpus46/47`、`CodexVIP53/54`

**支持配置**：
```json
{
  "ai": {
    "enabled": true,
    "primary_model": "TencentGLM5",
    "fallback_models": ["MiMoPro", "GLM-5-Turbo"],
    "timeout_ms": 10000,
    "max_retries": 2
  }
}
```

Web 面板里：下拉选一个 primary，多选 fallback（tag 输入框），timeout/retries 用数字输入。

### 3.2 宽松程度（Sensitivity）
一个**滑块**（0-100），内部映射为 thresholds：
- 0  = 极宽松（只有 0.95+ 才删）
- 50 = 默认（0.9/0.75/0.5/0.3）
- 100 = 极严格（0.7/0.5/0.3/0.15）

也支持 **专家模式**展开四个 threshold 独立调：
```json
{
  "ai": {
    "sensitivity": 50,
    "expert_mode": false,
    "thresholds": {
      "ban": 0.9,
      "mute": 0.75,
      "warn": 0.5,
      "flag": 0.3
    }
  }
}
```

### 3.3 新人定义（Watch Policy）
```json
{
  "ai": {
    "watch": {
      "enabled": true,
      "graduate_after_messages": 10,    // 前 N 条审核
      "graduate_after_days": 7,          // 加群 N 天
      "graduate_min_clean_messages": 5,  // 至少 N 条干净才毕业（防刷量）
      "recheck_suspicious": true,        // suspicious 消息管理员确认前不毕业
      "check_all_admins_excluded": true  // 管理员永不审
    }
  }
}
```

### 3.4 Prompt 自定义
```json
{
  "ai": {
    "prompt": {
      "version": "v1",                  // 版本号（历史可回滚）
      "system": "你是 Telegram 群广告识别器...",
      "custom_rules": [                 // 用户自定义规则（拼到 prompt 末尾）
        "这是一个技术交流群，不允许讨论币圈",
        "招聘信息必须包含公司名称和 HR 联系方式"
      ],
      "whitelist_patterns": [           // 匹配这些直接放行
        "^(\\+1|同意|支持)"
      ],
      "categories": ["招聘","交友","币圈","刷单","引流","政治","色情","正常"]
    }
  }
}
```

Web 面板有专门的 **Prompt 编辑器**页面：
- 左侧：system prompt（markdown 编辑器）
- 右侧：自定义规则列表（可增删拖动排序）
- 底部：**测试**区域，粘贴一条消息 + 选模型 → 看 AI 判决（不入库）
- 历史版本：保存时 version+1，可回滚

### 3.5 操作策略（Per-Category Actions）
不同类别不同处置：
```json
{
  "ai": {
    "category_actions": {
      "招聘": "warn",      // 招聘只警告（有些群允许）
      "交友": "mute",
      "币圈": "ban",
      "刷单": "ban",
      "引流": "delete_and_warn",
      "政治": "delete",
      "色情": "ban",
      "正常": "none"
    }
  }
}
```

### 3.6 成本控制
```json
{
  "ai": {
    "cost_control": {
      "daily_budget_cents": 500,        // 每天预算（5 元）
      "per_user_daily_limit": 50,       // 每用户每天最多调 50 次
      "skip_messages_shorter_than": 5,  // 短消息跳过
      "batch_window_ms": 500,           // 500ms 内合批
      "cache_ttl_hours": 24             // 相同消息缓存 24h
    }
  }
}
```

### 3.7 人审闭环
Web 面板 `/ai-review` 页面：
- 列出 `ai_decisions WHERE admin_override IS NULL ORDER BY confidence`
- 每条展示：消息原文、AI 判决、置信度、用户信息
- 三个按钮：
  - **确认**（confirm）→ 写 override + 学习到 prompt 示例集
  - **误判-这是正常消息**（false_positive）→ 取消处罚 + 记录反例
  - **漏判-其实是广告**（false_negative）→ 补处罚 + 记录正例
- 定期自动把高频反例/正例加到 prompt few-shot（需管理员审批）

---

## 4. 多模型 Provider 接入

### 4.1 统一接口
```go
type LLMClient interface {
    Check(ctx context.Context, req CheckRequest) (*CheckResult, error)
}

type CheckRequest struct {
    Model        string
    SystemPrompt string
    Messages     []Message      // 支持批量
    MaxTokens    int
    Temperature  float64
}

type CheckResult struct {
    Verdicts   []Verdict        // 一一对应输入 messages
    Model      string
    LatencyMs  int
    CostCents  float64
    PromptTokens, CompletionTokens int
}
```

### 4.2 Provider 实现
- **openai 兼容**（大部分国产走这个）：NewAPI / volcengine / zhipu / tencentcodingplan
- env 配置 `LLM_PROVIDERS` JSON：
```json
[
  {"name": "newapi", "base_url": "https://newapi.misaka.si/v1", "api_key_env": "NEWAPI_KEY", "models": ["TencentGLM5","MiMoPro"]}
]
```

面板根据配置列出可用模型。

---

## 5. 消息流整合（与 M3 关系）

```
新人发消息
    │
    ▼
[M3 Filter 先过] ─→ 命中关键词/链接 → 直接处理（不调 AI）
    │未命中
    ▼
[M5 AI 审核] ─→ 按 trust 状态决定是否调 AI
    │
    └→ AI 判决 → 写 ai_decisions + 执行 action + 更新 user_trust
```

M3 Filter 是**硬规则**，M5 AI 是**软规则**。硬规则优先（快、免费、确定）。

---

## 6. Web 面板页面（M4 + M5 合并后完整结构）

- `/` — Telegram Login
- `/dashboard` — 群数/今日违规/AI 成本/信任用户数
- `/groups` — 群列表
- `/groups/[id]` — 单群配置，tabs：
  - 基础信息
  - 验证策略（M1/M2）
  - 过滤策略（M3）
  - 警告策略（M3）
  - 反垃圾策略（M3 CAS）
  - **🆕 AI 审核策略（M5）**：模型选择、宽松程度、新人定义、prompt、成本
  - 审计日志
- `/violations` — 传统违规
- `/ai-review` — 🆕 AI 判决人审队列
- `/ai-costs` — 🆕 AI 成本统计（按日/按群/按模型）
- `/trust` — 🆕 用户信任列表（status 筛选，手动 graduate/ban）
- `/audit` — 配置审计
- `/prompt-editor` — 🆕 AI prompt 编辑与测试

---

## 7. 实施顺序

### M4（当前正在做）：基础 Web 面板
- Telegram Login + JWT
- 现有 M1~M3 配置的 UI
- violations / warnings / audit 展示
- 手动 ban/unban

### M5.1：信任系统 + 数据层
- user_trust / ai_decisions 表
- 消息进来时的信任判断钩子
- /trust 页面

### M5.2：AI 审核核心
- LLMClient 抽象 + openai 兼容 provider
- 异步 worker 池 + 批量
- 单模型 hardcode 跑通（先 TencentGLM5）

### M5.3：多模型 + 可配置
- Provider 管理
- 面板上的模型选择 + fallback 链
- sensitivity 滑块

### M5.4：Prompt 编辑器 + 测试
- /prompt-editor 页面
- 测试沙箱（不入库）
- 版本管理

### M5.5：人审闭环
- /ai-review 页面
- override 反馈
- few-shot 半自动学习

### M5.6：成本与稳定性
- /ai-costs 仪表盘
- 预算控制 + 限流
- 缓存 + 合批

---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| AI 误判老用户 | trust 系统只审 new 状态，trusted 用户完全跳过 |
| AI 接口不稳 | fallback 模型链 + 超时 10s + 失败降级（放行） |
| 成本失控 | 每日预算 + per-user 限流 + 消息哈希缓存 + 短消息跳过 |
| prompt 被污染 | 自定义规则只拼接到固定结构末尾，不允许覆盖 system prompt 骨架 |
| 管理员被误封 | ExemptAdmins=true（和 M3 一致） |
| 模型偏见 | 人审闭环持续修正 + 支持多模型对比 |

---

## 9. 成本估算（RFC 群 4-5k 人）

- 日活约 200
- 日新人约 20，每人前 10 条 = 200 次调用
- 老成员新消息不调 AI
- 主用 TencentGLM5（约 ¥0.001/次输入 200 tokens + 输出 100 tokens）
- **日均**：200 次 × ¥0.001 = ¥0.2
- **月均**：¥6
- 峰值（1000 次/天）：¥1/天，¥30/月

可接受。

---

## 10. 下一步选择

### 路径 A：M4 完整做完 → M5 分阶段推
优点：UI 骨架先有，后面改舒服；坏处：要再等 M4 落地（估计 30 分钟）

### 路径 B：M4 只做登录+AI 相关页面 → M5 尽快上
优点：重点突出；坏处：其他管理页面要等

**推荐 A**——M4 Codex 已经开干了，停不合适。M5 等 M4 完了再单独拉一次 Codex 做。
