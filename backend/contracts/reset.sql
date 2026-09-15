-- 录音棚重置脚本：record 之前执行（写操作用例会污染库，见 P4 教训）
-- 用法：docker exec -i ses-sender-mysql mysql -uroot -p*** ses_harness < backend/contracts/reset.sql
-- 随域推进追加清理语句（保持幂等：全部 DELETE/UPDATE，不做 DROP）
DELETE FROM users WHERE username LIKE 'corpus%';
