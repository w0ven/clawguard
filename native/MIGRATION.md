# 原生助手域迁移与安全回退

固定源：`82c3703daba218b36255132c9bf51ebc444c6480`。以下是交给独立发布者的步骤，实施包未执行生产操作。

## 单一权威及映射

| 数据 | 切换处理 | 切换后权威 |
|---|---|---|
| 群授权、chat_enabled、模型目录/角色引用/主备权重/群覆盖/共享容量、TG/TTS密钥 | 不修改、不重新生成；PG原样保留 | CG PG与现有配置入口 |
| 旧助手行为参数、群Prompt及全局Prompt | 连原记录版本先导出；运行改用固定SGB默认，不回写旧PG值 | native `control.sqlite3`；模型参数仍属CG |
| 最新目标/名称/画像、sample/distilled计数 | 原文逐字迁移至源 `Group.settings.speech_style`；导出检查RFC不低于已完成v10，初次生产激活再对比实时PG版本/目标/画像hash/计数 | native根topic源Group；不再PG与SQLite相互回灌 |
| 已过审、已送达、仍有效的原文 | 保留真实群/TG消息key、topic、角色、发送人、原时间；没有的旧媒体字段明确标记缺失，不推造 | 各topic原生源档案/热窗口/FTS/向量 |
| 无效、已过期、被遗忘来源 | 只在私有原始备份保留，不进入运行档案；忘记tombstone跨重做保留 | 原始PG/私有导出用于审计；不可召回 |
| 手工管理员长期知识 | 必须有真实操作人、有效且已验证、admin_base/admin_explicit服务端来源及long_term/current_group范围；关联TG来源还须当前有效/hash一致 | 源永久记忆表，保留作者和创建/更新时间；映射表可追旧PG ID |
| 自动学习/临时/未知权威事实、冲突、旧版本 | 不升级永久记忆，不删除PG；导出完整版本、来源与权限/过期状态 | 旧PG只读查看，不注入原生聊天 |
| 风格样本 | 当前目标最近200条完整保存；更多或旧目标留原始PG/备份 | 源SpeechStyleService，50/200/1000行为及compress角色 |
| 贴纸 | 有当前有效同topic原始来源才激活；保留文件ID/描述/别名/计数/时间；未知来源只备份 | 源topic贴纸库 |
| Wiki | 固定34全文/28指南一次导入，仅两个约定群；URL/hash/版本/批次授权 | 独立native WikiStore，永久保留直至管理员撤回 |

范围只包括当前授权且已启用助手/已有目标的群及两个知识目标群。不修改授权名单，不自动启用其他群。不调用任何模型、TG或Wiki网络。

## 可执行接口

在构建后的原生工作目录执行 `python -m clawguard_native.migration …`（本地隔离开发为 `cd native && uv run python -m …`）。`--snapshot` 对export/import/verify/inspect指JSONL文件；对backup-domain/restore-domain/verify-backup指备份目录。

1. 独立发布者先保存PG全库快照、现有镜像引用及助手相关配置/Prompt，不覆盖完整`.env`。确保无其他助手写入者。
2. 仅更新必要CG应用为本包构建版本，设置 `ASSISTANT_ENGINE=paused`。该模式不启动旧学习/回复/风格/索引/主动工作器，不接助手输入；群管入口不变。不要停PG、Redis、Caddy或其他基础服务。
3. 最终导出（数据库DSN仅从发布者私密环境变量读取）：
   ```sh
   python -m clawguard_native.migration export --production --snapshot /backup/assistant-final.jsonl
   python -m clawguard_native.migration inspect --snapshot /backup/assistant-final.jsonl
   python -m clawguard_native.migration import --snapshot /backup/assistant-final.jsonl --data /data
   python -m clawguard_native.migration verify --production --snapshot /backup/assistant-final.jsonl
   ```
   必需环境：`CG_MIGRATION_DATABASE_URL`（只读PG账号）、`CG_BROKER_URL=http://bot:8080/internal/native`、`ASSISTANT_BROKER_SECRET`。export/verify前后验证CG确实paused；拒绝重复覆盖导出文件。非production模式只接受本任务127.0.0.1隔离库名。
4. 核查manifest每群目标/画像hash/计数、导出行数/总hash、激活原文/手工记忆/保留未提升数量、Wiki34/28。RFC v10应是目标258605875、profile hash `ab5b669407c432cd937a46aa4ed494f2d40722d7c4c6b2e6f72b66499f68e212`、sample14/distilled14；若当前PG版本更高，必须用当前真值，不能旧v10反盖。测试群不做手动蒸馏。
5. 仅将CG模式改native并启动已接受的原生镜像，设置 `NATIVE_REQUIRE_MIGRATION=true`。宿主拒绝空域/未完成准备/第二个进程；初次生产激活再次向CG检查最新PG画像/目标/计数后才记录activated_at。校验失败先回paused，重新导出/准备，不删旧备份；不重新蒸馏。
6. 独立发布者按验收计划检查健康、源调用和两群知识，不由实施包代做生产发言。

准备阶段用独立临时目录完整执行源ORM导入，最后才提交prepared标记。中途失败会阻止宿主启动，重做不混入上一失败尝试的半成品。旧准备文件、原始导出全部保留。激活后禁止用任何不同旧快照覆盖原生真值。

## 回退：不把新真值强转成旧PG模型

原生永久记忆/Wiki/源调度状态不能被旧Go引擎完整表达。**不得切legacy并重新启用普通聊天自动事实学习，不得用旧卷覆盖运行后的新记忆。** 主控已明确：回退可暂不提供新版助手能力，但群管必须可恢复。因此有两个独立退路：新版CG还能运行时用paused；新版Go本身不健康时用下述预构建的**旧基线群管专用救援镜像**，不依赖新版Go启动。原生新域完整保留。

1. 新版CG健康时先改paused；新版Go不能运行则先停止其bot应用。停止仅原生助手单元，排空其写入后备份当前域（包含切换后新增/修改/删除、画像/采样、Wiki授权、持久发送清理/调度状态）：
   ```sh
   python -m clawguard_native.migration backup-domain --data /data --snapshot /backup/native-before-rollback
   python -m clawguard_native.migration verify-backup --snapshot /backup/native-before-rollback
   ```
   备份须在域外新目录，SQLite使用backup API包含已提交WAL，逐库integrity_check、逐文件hash。运行中拒绝备份。
2. 若只回退原生应用镜像且该已验收镜像兼容相同源schema，使用**当前原生域**恢复，不回PG旧快照。首版没有旧的原生镜像时保持paused，待修复通过，不冒称旧Go能等价提供新知识。
3. 演练/灾难恢复只还原到**新空目录/新卷**，保留当前域：
   ```sh
   python -m clawguard_native.migration restore-domain --snapshot /backup/native-before-rollback --data /restored-data
   ```
   核对原生运行后知识/删除/画像/计数及Wiki，再由发布者单独变更助手卷绑定与镜像引用；不回滚完整`.env`，不撤销PG加法迁移36，不重建基础服务。
4. **新版Go无法健康运行：旧群管救援**。原版abe2089没有助手总停开关：即使chat_enabled/learning_enabled=false，过审贴纸仍在开关前写入；retentionWorker也无条件小时清理。因此不谎称仅关几个policy字段足够，也不做DB权限/触发器/数据删除。
   - 发布前执行 `sh native/scripts/prepare-rescue.sh <新临时目录>`，只从本地固定Git对象 `abe20897450fa46d1693aff476e6a6c15ca9d1f5` 导出旧bot代码。仅两处安全补丁：真实Service不构造GroupAssistant（因此没有任何旧助手工作器/贴纸/编辑失效/学习链），不注册群/全局助手API（防旧面板重新写助手域）。其余旧群管、权限、模型目录、WebHook、基础服务代码逐字保持基线。补丁前后hash记在生成的rescue-manifest.json；**它是带两处明确停助手补丁的旧群管救援构建，不冒称未经修改的旧镜像**。
   - 在该目录bot下按原Dockerfile预构建并独立验收救援镜像，将其immutable digest及保留的旧web digest放入发布记录。实施阶段本地镜像为 `openbear/clawguard-guard-rescue:abe2089-assistant-off`（未推送，发布者需自行固定正式digest）。不要等事故发生后依赖新版Go编译或临时改库。
   - 备份/停止新原生域后，使用 `native/rollback-guard-only.compose.yml`，仅为本次命令提供 `CLAWGUARD_GUARD_RESCUE_IMAGE` 和 `CLAWGUARD_ROLLBACK_WEB_IMAGE`；执行 `docker compose -f docker-compose.yml -f native/rollback-guard-only.compose.yml up -d --no-deps bot web`。该override直接运行旧`clawguard`二进制，跳过旧migrate，保留已通过兼容验证的PG加法schema36；不重建PG/Redis/Caddy/桥/隧道，不改任何助手policy/profile/模型开关或整份.env。
   - 救援时群管恢复、助手页面API不可用（不能开回旧自动学习），原生卷不挂到救援bot，也不强转回旧PG。新功能恢复须修复验收后换回新CG/native镜像，仍绑定**事故时保留的最新原生域**；activated_at阻止旧PG画像再次覆盖。
   - 实测 `sh native/scripts/test-rescue-runtime.sh <新证据目录>` 用救援真实镜像、全新PG1–36/Redis、Docker `--internal` 网络＋假TLS Telegram（无真实外网/模型/TG）：`/readyz`与`/healthz`成功，旧policy仍chat/learning/target/cold全开启也不产生新事实或贴纸，旧原文/配置不变，未挂载任何原生域。另有两项构造/API门禁及原有群管/群授权回归通过。该退路不依赖新版Go的paused实现，也不要求任何新数据库权限操作。

## 隔离证据

`sh native/scripts/test-migration.sh`：使用一次性本机PG16 tmpfs数据库，执行真实迁移1–36；验证来源/遗忘/topic/作者、最新画像、重复准备、失败中断重做、激活后拒旧覆盖、运行后新增和删除永久记忆/新画像的完整备份还原、Wiki仍可检索；不调用模型或TG。测试容器退出即清理。

另有 `test_activation.py` 和 `TestNativeMigrationRejectsChangedProfileOrTarget` 验证初次激活最新值检查；`TestNativePausedAndCallbackGrantBoundary` 验证paused不启动旧工作器。本目录证据仍待主控独立验收，不代表生产迁移已经执行。
