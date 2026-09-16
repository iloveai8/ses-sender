-- 录音棚重置脚本：record 之后 / diff 之前执行（写操作用例会污染库，P4 教训）
-- 用法：docker exec -i ses-sender-mysql mysql --default-character-set=utf8mb4 -uroot -p*** ses_harness < backend/contracts/reset.sql（-e 内联中文会 mojibake，一律文件管道）
-- 教训：-e 内联中文会被字符集搞成 mojibake；一律文件管道+utf8mb4
-- 语义 = 清理语料写残留 + 播种固定数据（幂等）
DELETE FROM users WHERE username LIKE 'corpus%';
DELETE FROM contact_groups WHERE name LIKE 'corpus%';
DELETE FROM contacts WHERE email LIKE 'corpus%';
INSERT INTO contact_groups (id, name, description, user_id) VALUES (1, 'seed-group', 'harness fixed seed', 1)
ON DUPLICATE KEY UPDATE name=VALUES(name), description=VALUES(description);
INSERT INTO contacts (id, email, name, attributes, group_id) VALUES
 (1, 'seed1@harness.local', 'SeedOne', '{"city":"上海","level":"VIP"}', 1),
 (2, 'seed2@harness.local', 'SeedTwo', NULL, 1)
ON DUPLICATE KEY UPDATE email=VALUES(email), name=VALUES(name), attributes=VALUES(attributes);
INSERT INTO email_templates (id, name, ses_name, subject, html_body, text_body, user_id, created_at)
VALUES (1, 'seed-tpl', 'u1_seedtpl', 'Seed Subject', '<p>seed</p>', ' ', 1, UTC_TIMESTAMP())
ON DUPLICATE KEY UPDATE name=VALUES(name), subject=VALUES(subject);
DELETE FROM email_templates WHERE name LIKE 'corpus%';
DELETE FROM system_settings WHERE `key` IN ('ai_models');
-- P8 固定种子：退订页系统级标题（unsub-get-valid 金标准依赖它存在）
INSERT INTO system_settings (`key`, value) VALUES ('unsub_page_title', '语料标题')
ON DUPLICATE KEY UPDATE value=VALUES(value);
DELETE FROM email_blacklist WHERE email LIKE 'corpus%';

-- P7 种子：批次/明细（固定历史日期，避开 dashboard 日界漂移）
INSERT INTO sending_jobs (id, user_id, batch_id, template_name, template_id, group_name, group_id,
  source_email, from_name, reply_to, total_contacts, sent_count, total_batches, status,
  error_message, configuration_set, created_at, finished_at) VALUES
 (1, 1, 'batch-seed000001', 'seed-tpl', 1, 'seed-group', 1, 'admin@seed.local', 'SeedSender',
  'reply@seed.local', 2, 2, 0, 'success', NULL, '', '2026-01-01 09:00:00', '2026-01-01 09:00:05'),
 (2, 1, 'batch-seed000002', 'seed-tpl', 1, 'seed-group', 1, 'admin@seed.local', 'SeedSender',
  'reply@seed.local', 1, 0, 0, 'failed', 'seed fail reason', '', '2026-01-02 10:00:00', '2026-01-02 10:00:03')
ON DUPLICATE KEY UPDATE status=VALUES(status);
INSERT INTO sending_job_details (id, job_id, batch_id, message_id, recipient, send_status,
  delivery_status, delivery_time, open_count, first_open_time, click_count, created_at) VALUES
 (1, 1, 'batch-seed000001', 'seedmsg000001', 'seed1@harness.local', 'Success', 'Delivery', '2026-01-01 09:00:04', 1, '2026-01-01 10:00:00', 0, '2026-01-01 09:00:00'),
 (2, 1, 'batch-seed000001', 'seedmsg000002', 'seed2@harness.local', 'Success', 'Delivery', '2026-01-01 09:00:04', 0, NULL, 1, '2026-01-01 09:00:00'),
 (3, 2, 'batch-seed000002', NULL, 'seed1@harness.local', 'Failed', NULL, NULL, 0, NULL, 0, '2026-01-02 10:00:00')
ON DUPLICATE KEY UPDATE send_status=VALUES(send_status);
-- 定时任务（paused 防录音棚调度器真执行）
INSERT INTO scheduled_jobs (id, user_id, template_id, group_id, template_name, group_name,
  schedule_type, scheduled_time, cron_hour, cron_minute, status, next_run_at, last_run_at,
  run_count, last_batch_id, created_at, updated_at) VALUES
 (1, 1, 1, 1, 'seed-tpl', 'seed-group', 'daily', '2026-01-01 09:00:00', 9, 0, 'paused',
  '2026-01-02 09:00:00', '2026-01-01 09:00:00', 1, 'batch-seed000001', '2026-01-01 08:00:00', '2026-01-01 08:00:00')
ON DUPLICATE KEY UPDATE status=VALUES(status);
-- 退订记录
INSERT INTO unsubscribe_list (id, email, source_email, reason, unsubscribed_at) VALUES
 (1, 'unsub-seed@harness.local', 'admin@seed.local', 'too_frequent', '2026-01-03 12:00:00')
ON DUPLICATE KEY UPDATE reason=VALUES(reason);
DELETE FROM unsubscribe_list WHERE email = 'seed1@harness.local';
DELETE FROM sending_jobs WHERE batch_id LIKE 'batch-%' AND batch_id NOT LIKE 'batch-seed%';
DELETE FROM sending_job_details WHERE batch_id LIKE 'batch-%' AND batch_id NOT LIKE 'batch-seed%';
UPDATE users SET email = 'admin@seed.local' WHERE username = 'admin';
