# ses-sender v2 发送引擎详细设计（ENGINE-DESIGN）

> **文档定位**：详细设计①/②。delivery 模块的完整规格——状态机、并发模型、限速算法、断点恢复证明、事件管道。
> **版本**：v0.1（待评审③）｜**语义基准**：现网 `core/sender.py` + `sending/service.py`，逐条对等，实现语言换 Go。
> 本文是全系统最高风险区的规格，P9 编码逐条验收。

---

## 1. 双状态机

### 1.1 批次状态机（sending_batches.status）

```
                ┌────────────────────────────────────────────┐
                │                                            │
  queued ──► sending ──┬──► success   (全部明细终态且无失败)   │
   ▲          │        ├──► partial   (有失败, error="N 封发送失败")
   │          │        └──► failed    (active>0 且全败)       │
   │          │                                            │
   │          └── 特例：active 明细数=0（全被退订过滤）──► success(建单即刻)│
   │                                                      │
   └── 卡死自愈路径：sending 停留且真 Pending=0 时由 Scanner 判定终态 ──┘
```

**触发者**：queued→sending 由 Scanner；sending→终态由 Worker 组（正常路径）或 Scanner（自愈路径）。

### 1.2 明细状态机（sending_records.send_status）

```
Pending ──► Success   ① 发送成功  ② 收到任意回执事件时的修正（Pending+有回执→Success）
Pending ──► Failed    ① 发送异常  ② 黑名单拦截（error="[Blacklisted] 邮箱在黑名单中"）
建单即 Unsubscribed   （退订过滤命中，不进引擎队列）
Success/Failed = 终态，引擎不再触碰（防重的根基）
```

**delivery_status 独立维度**：NULL→Delivery/Bounce/Reject/Sent（事件驱动，与 send_status 平行推进）。

## 2. 并发模型

```
┌─ Scanner goroutine（每 5s 一轮）─────────────────────────────┐
│  ①自愈 ②取批次 ③入队                                          │
└────────────┬────────────────────────────────────────────────┘
             ▼  taskCh = make(chan SendTask, 并发×速率×4)
┌─ Worker×N goroutine（默认 2）────────────────────────────────┐
│  for task := range taskCh { 限速等待 → sendOne(task) }        │
└────────────┬────────────────────────────────────────────────┘
             ▼
      MailProvider.Send() → 明细行状态推进（独立短事务）
```

**goroutine 清单**（all/worker 模式启动，api 模式全部不启动）：

| goroutine | 周期 | 职责 |
|-----------|------|------|
| Scanner | 5s | 自愈 + 取批次 + 入队 |
| Worker×N | 事件驱动 | 发送 + 明细状态推进 |
| EventSource | 20s 长轮询 | SQS 拉事件 → sink |
| Scheduler | 30s（先睡后查） | 到期定时任务 → BatchCreator |
| BlacklistRefresher | 60s（先睡后刷） | 全量重载黑名单 Set |

**DB session 纪律**：每个 goroutine 内每个工作单元独立短 session（Scanner 一轮一个、Worker 每封一个、事件每条一个）——对齐 Python"每操作独立 SessionLocal"语义，杜绝跨 goroutine 共享 *sql.DB 之外的会话状态（*sql.DB 本身并发安全，连接池共享）。

## 3. Scanner 逐轮算法（5 秒一轮，顺序固定）

```
步骤① 自愈（对全部 status=sending 的批次）：
   a. UPDATE ... SET send_status='Success'
     WHERE batch_id=? AND send_status='Pending' AND delivery_status IS NOT NULL
     （有回执却还 Pending → 已发出，修正）
   b. 对每批次统计真 Pending 数（Pending 且 delivery_status IS NULL）：
      =0 → 判终态：failed=total → failed；failed>0 → partial("N 封发送失败")；
           否则 success；sent_count=非Unsubscribed明细数；finished_at=utcnow
步骤② 取新批次：SELECT * WHERE status='queued' ORDER BY id LIMIT 5
      逐个：置 sending（先提交，再入队——保证崩溃后自愈路径可见）
      特例：active 明细（Pending 数）=0 → 直接置 success + finished_at
步骤③ 入队：逐明细构造 SendTask：
      - 模板解析：先 template_id，miss 则 (user_id, template_name)；subject 兜底=模板名，html 兜底""
      - 附件：模板附件行 → [{file_name, file_path, content_type}]
      - reply_to = user.contact_email or job.source_email
      - from_name = job.from_name → user.sender_name → 邮箱@前缀（strip）
      - group_id 缺失 → 按 (user_id, group_name) 反查
      - 每明细：跳过 Unsubscribed；生成退订 token；联系人查 (email,group_id) → miss 则 (email)；
        name 兜底 "Customer"；attributes JSON 解析失败忽略
      - taskCh 投递（超时 30s）；队满 → 本轮中止（记 warning，剩余批次留下轮——批次已 sending，
        待自愈路径或下轮重新入队处理，Pending 明细仍在）
```

**SendTask 结构**（对齐 Python dataclass 全字段）：`job_id, batch_id, recipient, name, source_email, from_name, reply_to, subject_tpl, html_tpl, text_tpl(引擎恒空), attributes, config_set, tags{batch_id,user_id}, unsub_url, attachments, detail_id`。

## 4. Worker 与 sendOne 算法

```
限速：每 Worker 独立计数器——距上次重置 ≥1.0s 则清零；计数 ≥ message_rate 则睡到本秒末
      （对齐 Python time.monotonic 对齐语义；Go 用 time.Since 对齐）
sendOne(task)：
  ① 黑名单：Blacklist.IsBlacklisted(recipient)（小写化+trim）命中 → 明细=Failed/[Blacklisted]...，return
  ② 防重：独立 session 重查明细（优先 detail_id，否则 batch+recipient+Pending），
     存在且 send_status != Pending → 跳过（不改状态）——崩溃恢复后防双发的关键
  ③ 渲染：变量替换顺序敏感：{{name}}→{{email}}→{{unsubscribe_url}}(空→"#")→自定义属性按 map 序
  ④ 发送参数（SES v2 send_email）：
     FromEmailAddress = format_sender(from_name, source_email)   ← formataddr 语义手写移植
     ReplyToAddresses = [reply_to or source_email]
     Content.Simple = Subject + Body.Html（引擎不发送文本正文——照抄）
     Headers = List-Unsubscribe + List-Unsubscribe-Post: One-Click（仅 unsub_url 非空）
     ConfigurationSetName（config_set 非空时）+ EmailTags[{batch_id},{user_id}]
     Attachments（文件存在时）：RawContent 原始字节 + BASE64 传输编码 + ATTACHMENT 处置
  ⑤ 成功 → updateDetailStatus(batch, recipient, "Success", "", message_id)
     失败 → 错误串格式化 → "Failed"；含 Throttling/Rate exceeded → sleep 2s
updateDetailStatus：UPDATE ... WHERE batch_id=? AND recipient=? AND send_status='Pending'
     （同 batch 同邮箱全部 Pending 行一起更新）；affected>0 → job.sent_count += affected（独立事务提交）
```

**错误串格式化**（send_error 落库格式=契约）：SDK 错误先复刻 boto3 字符串 `An error occurred (Code) when calling the Op operation: Msg`，再正则提取为 `[Code] Msg`；提取失败截前 200 字符。

## 5. 限速与配额数学

```
启动时一次性快照：quota = MailProvider.GetQuota()（失败 → MaxSendRate=1）
SENDER_MESSAGE_RATE=0（默认自动）时：message_rate = max(floor(MaxSendRate / concurrency), 1)
队列容量            = concurrency × message_rate × 4
可选固定窗口（两参数均>0 启用）：窗口期内计数 ≥ 上限 → 拒绝并等待（50ms 轮询，30s 超时→
                                明细=Failed/"Rate limit timeout"）
配额快照不刷新（进程生命周期内恒定）——照抄现状
```

## 6. 断点恢复证明（崩溃场景枚举）

| 崩溃时机 | 数据状态 | 重启后行为 | 结论 |
|---------|---------|-----------|------|
| 建单事务中 | 事务未提交，无痕迹 | 用户重发 | 无害 |
| 建单后、入队前 | 批次 queued，明细 Pending | Scanner 正常拾取 | 无害 |
| 批次置 sending 后、入队中 | 批次 sending，部分明细 Pending | 明细仍 Pending：防重检查②放行 → 正常发送；已入队未发的在内存丢失 → Pending 残留 → **Scanner 自愈①a 无法修正（无回执）→ 卡到…** 见下行 | ⚠️ 唯一边界 |
| 上述残留 | 批次 sending，Pending 且永远无回执 | 与现网 Python 同样行为：批次停留 sending（现网此态由"重发同批次不可行"兜底，用户可查看明细人工判断）。**照抄现状，登记已知边界，不引入新机制** | 对等 |
| 发送成功后、状态更新前 | 明细 Pending，但邮件已发出 | 防重检查②放行 → **重发一次**（现网同样风险窗口）；若收件人打开/送达事件先到 → 自愈①a 修正 Success 且防重拦截 | 与现网对等（at-least-once 语义） |
| 状态更新后、计数前 | 明细 Success，sent_count 少计 | 自愈路径终态判定时 **sent_count=非 Unsubscribed 数** 重算覆盖 | 无害（自愈兜底） |
| 事件落库中 | 事件未 ack → SQS 重投 | 重新处理（状态类幂等；计数类重复累加=毛边#9） | 对等 |

**结论**：引擎整体为 at-least-once 投递语义（与现网一致）；"不重发"的严格保证受限于"发送成功与状态落库"间的窗口——这是现网既有语义，照抄。

## 7. 事件管道（EventSource → 落库）

```
SQS ReceiveMessage(Max=10, Wait=20s, MessageSystemAttributeNames=[ApproximateReceiveCount])
  │ SNS 包装（body.Type=Notification）→ 再解析 Message 字符串
  │ SubscriptionConfirmation / 非 JSON → 直接删除（跳过）
  ▼
归一化 CanonicalEvent：{eventType, mail.messageId, delivery.timestamp, bounce{type,subtype,
  recipients[0].diagnosticCode}, complaint.timestamp, open.timestamp, click.timestamp}
  ▼ 幂等落库（每事件独立事务）：
  按 message_id 查明细（miss → 忽略）
  明细 send_status=Pending → 先置 Success
  eventType（统一 .upper()）分派：
    DELIVERY  → delivery_status=Delivery, delivery_time=timestamp(解析失败=now)
    BOUNCE    → delivery_status=Bounce, bounce_type/subtype/message, delivery_time=timestamp
    COMPLAINT → complaint_time
    OPEN      → open_count+=1, first_open_time??=timestamp
    CLICK     → click_count+=1, first_click_time??=timestamp
    REJECT    → delivery_status=Reject
    SEND      → delivery_status ??= Sent
  ▼ 成功 → DeleteMessage
    失败 → ApproximateReceiveCount ≥3 ? 删除(毒消息) : 留队重投（外层轮询异常 sleep 5s）
```

## 8. Scheduler（定时任务）

```
循环：sleep 30s → 查 status='active' AND next_run_at <= utcnow → 逐个执行
执行：复用 campaign.BatchCreator（同 send-bulk 逻辑）
      成功：last_batch_id=结果批次, error_message=NULL
      失败：error_message=HTTP detail 或 str(e)
      收尾：last_run_at=now, run_count+=1
      once → status=completed, next_run_at=NULL
      其他 → next_run_at=_calc_next_run(after=now)
循环内异常 sleep 10s 继续（单任务失败不阻塞其他任务）
```

**_calc_next_run 全量边界用例表**（表驱动测试的验收清单，全 UTC naive）：

| 用例 | 输入 | 期望 |
|------|------|------|
| once 未来 | scheduled_time > now | scheduled_time |
| once 过去 | scheduled_time ≤ now | nil（但 once 到点执行后直接 completed，此分支只在重算时出现） |
| daily 未到点 | 09:00, now=08:00 | 今日 09:00 |
| daily 已过点 | 09:00, now=10:00 | 明日 09:00 |
| weekly 今天未到 | dow=3(周四), now=周三 | 明日 base |
| weekly 已过 | dow=0(周一), now=周三 | +7 天 | 
| weekly 未指定 | day_of_week=nil | 按 0（周一）处理 |
| monthly 正常 | dom=15 | 本月 15 日 |
| monthly 超月末 | dom=31, 2 月 | 28（clamp 到 monthrange，再异常兜底 28） |
| monthly dom=0/nil | | 按 1 号 |
| monthly 已过 | dom=1, now=15 | 下月 1 日（12 月→次年 1 月） |

## 9. 优雅停机（Go 版新增能力，Python 版没有）

```
SIGTERM → context cancel：
  Scheduler/EventSource/BlacklistRefresher：检查点退出（当轮完成）
  Scanner：停止取新批次
  Worker：taskCh close 后排空（在途批次 ≤ 队列容量 封，秒级）
  超时 25s（ECS 默认 stopTimeout 30s 内）强制退出
```

## 10. 测试规格（P9 验收）

1. **单测**：状态机全转移、_calc_next_run 上表 11 用例、错误串格式化、变量替换顺序、format_sender 四态、毒消息阈值
2. **fake SES 集成**：MailProvider 注入 fake（断言 From/ReplyTo/Headers 条件/Tags/附件），驱动完整批次 → 明细终态断言
3. **断点恢复演练**：在"入队中/发送后落库前"两个时机注入 panic → 重启 → 断言行为符合 §6 表格
4. **契约对拍**：同种子库 + 同 fake SES 双实现（Python/Go 各一）→ 终态 SQL diff 为空
