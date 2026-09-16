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
