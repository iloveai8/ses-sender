# ses-sender v2 数据结构设计（DATA-MODEL）

> **文档定位**：概要设计③/④。回答"数据怎么存"——11 张表全字段定义、索引、goose 基线方案、与 Python 共存策略。
> **版本**：v0.1（待评审②）｜**核心原则：过渡期表结构零变更（零 DDL）**，Go 版直读直写现库。
> 字段来源：Alembic 迁移链（head `e3f4a5b6c7d7`）+ ORM 模型勘察，与生产 `SHOW CREATE TABLE` 等价。

---

## 1. ER 总览（箭头=外键引用，全部逻辑外键，无数据库级 FK 约束——现状照抄）

```
users ──┬──► contact_groups ──► contacts
        ├──► email_templates ──► template_attachments
        ├──► sending_batches ──► sending_records
        ├──► scheduled_jobs
        └──► unsubscribe_list / email_blacklist（无用户关联，全局）
system_settings（独立 KV）
```

## 2. 表清单（11 张）

### 2.1 users（账户）

| 列 | 类型 | 约束/默认 | 说明 |
|----|------|----------|------|
| id | INT AI | PK | |
| username | VARCHAR(100) | **UNIQUE** ix | 登录名，JWT sub |
| display_name | VARCHAR(255) | | 显示名 |
| hashed_password | VARCHAR(255) | | bcrypt `$2b$12$` |
| email | VARCHAR(255) | | 发信邮箱（SES 已验证身份） |
| contact_email | VARCHAR(255) | NULL | 收件邮箱（Reply-To） |
| is_admin | BOOL | False(ORM层) | |
| is_active | BOOL | | 禁用=登录 403 |
| daily_send_limit | INT | 1000(ORM层) | 空值语义回退 1000（应用层） |
| sender_name | VARCHAR(255) | NULL | 发件人显示名 |
| unsub_config | TEXT | NULL | 退订页自定义 JSON |
| created_at | DATETIME | | naive UTC |

### 2.2 contact_groups / contacts（受众）

```
contact_groups: id PK·ix | name VARCHAR(255) ix | description VARCHAR(500) | user_id INT
contacts:       id PK·ix | email VARCHAR(255) ix | name VARCHAR(255)
                | attributes TEXT NULL（自定义属性 JSON 字符串）
                | group_id INT（删组级联删联系人——应用层行为）
                注意：无 (email, group_id) 唯一约束——允许重复导入（现状照抄，去重在建单时做）
```

### 2.3 email_templates / template_attachments（模板）

```
email_templates:      id PK·ix | name VARCHAR(255) ix | ses_name VARCHAR(255) UNIQUE
                      | subject VARCHAR(500) | html_body TEXT | text_body TEXT
                      | user_id INT | created_at
template_attachments: id PK | template_id INT ix（无FK） | file_name VARCHAR(255) NOT NULL
                      | file_path VARCHAR(500) NOT NULL | content_type VARCHAR(128) NOT NULL
                      | file_size INT DEFAULT 0 | created_at DEFAULT now()
```

### 2.4 sending_batches（现名 sending_jobs，Go 侧模型用新名、表名不变）★ 核心表

| 列 | 类型 | 说明 |
|----|------|------|
| id | INT AI PK·ix | |
| user_id | INT ix | |
| batch_id | VARCHAR(64) **UNIQUE** ix | `batch-{12hex}` |
| template_name / template_id(NULL) / group_name / group_id(NULL) | | 快照（模板/客群可后删，批次仍可追溯） |
| source_email | VARCHAR(255) | 本批发件身份 |
| from_name | VARCHAR(255) NULL | 发件人名快照（建单时定格） |
| reply_to | VARCHAR(255) NULL | |
| total_contacts | INT 0 | 含退订跳过者（配额口径） |
| sent_count | INT 0 | 引擎推进 |
| total_batches | INT 0 | 遗留列（现网恒 0，照抄） |
| status | VARCHAR(50) 'queued' | 状态机：queued/sending/success/partial/failed |
| error_message | TEXT NULL | |
| configuration_set | VARCHAR(255) NULL | SES 概念列（Go 写入同值；渠道抽象后第二步处置） |
| created_at / finished_at(NULL) | DATETIME | |

### 2.5 sending_records（现名 sending_job_details）★ 事件落点

| 列 | 类型 | 说明 |
|----|------|------|
| id | INT AI PK | |
| job_id | INT ix | → sending_batches.id |
| batch_id | VARCHAR(64) ix | 冗余（事件回写匹配加速） |
| message_id | VARCHAR(128) NULL **ix** | **回执事件匹配键**（SES 返回的不透明串） |
| recipient | VARCHAR(256) ix | |
| send_status | VARCHAR(32) 'Pending' | Pending/Success/Failed/Unsubscribed |
| send_error | TEXT NULL | `[Code] msg` 格式 |
| delivery_status | VARCHAR(32) NULL | Delivery/Bounce/Reject/Sent（**字面量=契约**） |
| delivery_time | DATETIME NULL | |
| bounce_type / bounce_subtype | VARCHAR(64) NULL | SES Permanent/Transient... |
| bounce_message | TEXT NULL | 诊断码 |
| open_count | INT 0 / first_open_time NULL | 打开累加+首次 |
| click_count | INT 0 / first_click_time NULL | 点击累加+首次 |
| complaint_time | DATETIME NULL | 投诉 |
| created_at | DATETIME DEFAULT now() | |

**已有索引（迁移核实，勿动）**：ix_sending_job_details_{batch_id, message_id, recipient, job_id}

### 2.6 scheduled_jobs（定时）

```
id PK | user_id ix | template_id | group_id | template_name | group_name
schedule_type VARCHAR(16)（once/daily/weekly/monthly）| scheduled_time DATETIME NOT NULL
cron_hour INT 9 | cron_minute INT 0 | day_of_week NULL | day_of_month NULL
status VARCHAR(16) 'active'（active/paused/completed/cancelled）
next_run_at DATETIME ix | last_run_at NULL | run_count INT 0 | last_batch_id NULL
error_message NULL | created_at | updated_at
```

### 2.7 unsubscribe_list（退订）

```
id PK | email VARCHAR(255) NOT NULL ix | source_email VARCHAR(255) NOT NULL ix
reason VARCHAR(32) 'one-click' | unsubscribed_at DEFAULT now()
UNIQUE (email, source_email)   ← 退订对幂等的关键
```

### 2.8 email_blacklist（黑名单）

```
id PK | email VARCHAR(255) NOT NULL UNIQUE ix | reason VARCHAR(500) ''
created_by VARCHAR(100) 'admin' | created_at DEFAULT now()
```

### 2.9 system_settings（系统 KV）

```
id PK | key VARCHAR(128) UNIQUE ix | value TEXT NULL | updated_at DEFAULT now() ON UPDATE now()
```

键域 ~40 个：ai_provider / bedrock_*×7 / openai_*×3 / image_*×8 / unsub_page_*×7 / sso_*×11 / ai_models（JSON 整包）。

## 3. 状态机（数据层面的两条生命周期）

```
批次：  queued ──► sending ──► success
                      ├──► partial（部分明细失败，error_message="N 封发送失败"）
                      └──► failed（全败）
明细：  Pending ──► Success（发送成功 或 收到任意回执事件的修正）
        Pending ──► Failed（发送异常/黑名单拦截）
        建单即 Unsubscribed（退订过滤命中，永不发送）
delivery_status（独立维度）：NULL ──► Delivery/Bounce/Reject/Sent（事件驱动）
```

## 4. goose 基线方案

```
1. 生成：空库跑 alembic upgrade head → mysqldump --no-data → 整理为 migrations/00001_baseline.sql
2. 引导（启动时，失败仅告警不阻塞——对齐 Python 行为）：
   无 alembic_version 且无 goose 表  → 全新库，goose up
   有 alembic_version==head 且无 goose 表 → 建 goose 表并把 baseline 标记已应用（零 DDL）
   有 goose 表                        → goose up 增量
   alembic_version 落后              → 报错日志+人工介入（冻结期不允许出现）
3. 冻结约定：过渡期零 schema 变更；切换完成后新迁移从 goose 版本 2 起，alembic 退役
```

## 5. Go 侧数据访问纪律（sqlc）

- 每模块 `db/queries.sql` 手写与 Python ORM **等价的 SQL**（含 `ORDER BY id DESC`、LIMIT/OFFSET、`ilike`→`LIKE` 大小写整理等价语义）
- `emit_json_tags=false`：序列化一律手写 DTO（契约字段名含大小写混用，杜绝生成物泄漏）
- 时间：DSN `parseTime=true&loc=UTC&charset=utf8mb4`；应用层一律 `time.Now().UTC()`；JSON 序列化为 Python isoformat 等价格式（无 Z）——保契约
- 动态 WHERE（email-details 筛选）：`WHERE (?='' OR recipient LIKE ...)` 参数化模式，禁止字符串拼接

## 6. 与 Python 共存的读写边界（过渡期）

| 数据 | Python（v1） | Go（灰度中） |
|------|-------------|-------------|
| 批次/明细 | 引擎写（发送状态、事件回写） | API 写（建单 queued 行）；灰度期不跑引擎 |
| 退订/黑名单/设置/用户 | API 写 | API 写（灰度路径）——**同库同表，靠"单写者语义"**：每行同一时刻只有一个系统在改 |
| 附件/图片文件 | 本地盘（任务临时盘） | 同 WORKDIR 路径读写（镜像路径已对齐） |

双写冲突面分析：过渡期同一 API 请求只会打到其中一个后端（ALB 加权分流），行级双写不存在；唯一并发点是两进程同时跑黑名单缓存刷新（只读）与 Python 引擎消费 Go 建的批次（设计意图）。
