#!/usr/bin/env bash
# 录音棚一站式脚本——根治"相对路径/工作目录"坑：脚本内部自锚定目录，怎么调用都不会漂
# 用法：
#   bash contracts/studio.sh record   # 起 Python(:8010) → 全量录 golden → 复位库 → 关棚
#   bash contracts/studio.sh diff     # 起 Go(:8011) → 全量对拍（退出码=结果）→ 关服务
#   bash contracts/studio.sh reset    # 仅复位录音棚库
# 环境变量：HARNESS_DB_PASS（本地 MySQL root 密码，默认 123456）
#           PY_DIR（Python worktree 的 backend 目录，默认 /d/go/src/ses-py/backend）
set -euo pipefail

BACKEND="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"   # 本脚本所在 backend/
PY_DIR="${PY_DIR:-/d/go/src/ses-py/backend}"
DBPASS="${HARNESS_DB_PASS:-123456}"
SECRET="harness-secret-key-2026"
DBURL="mysql+pymysql://root:${DBPASS}@localhost:3306/ses_harness"

mysql_exec() { docker exec -i ses-sender-mysql mysql --default-character-set=utf8mb4 -uroot -p"${DBPASS}" ses_harness 2>/dev/null; }

wait_http() { # $1=url $2=超时秒
  for _ in $(seq 1 "$2"); do
    sleep 1
    [ "$(curl -s -o /dev/null -w '%{http_code}' "$1" 2>/dev/null || true)" = "200" ] && return 0
  done
  echo "[studio] 等待 $1 超时"; return 1
}

kill_port() { # $1=端口
  local pid
  pid=$(netstat -ano | grep ":$1" | grep LISTENING | awk '{print $5}' | head -1 || true)
  [ -n "$pid" ] && taskkill //F //PID "$pid" >/dev/null 2>&1 || true
}

cmd="${1:-}"
case "$cmd" in
reset)
  mysql_exec < "$BACKEND/contracts/reset.sql" && echo "[studio] 录音棚已复位"
  ;;
record)
  [ -d "$PY_DIR" ] || { echo "[studio] Python worktree 不存在: $PY_DIR（git worktree add ../ses-py v0.1.5）"; exit 1; }
  kill_port 8010 || true
  (cd "$PY_DIR" && DATABASE_URL="$DBURL" SECRET_KEY="$SECRET" ENABLE_SENDER=false \
    SQS_QUEUE_URL= UNSUBSCRIBE_BASE_URL= PYTHONUTF8=1 \
    python -m uvicorn main:app --host 127.0.0.1 --port 8010 > "$BACKEND/../.py-harness.log" 2>&1 &)
  wait_http http://localhost:8010/ 60
  (cd "$BACKEND" && ./bin/harness.exe -mode record -base http://localhost:8010 \
     -corpus contracts/corpus.yaml -golden contracts/golden) | tail -3
  mysql_exec < "$BACKEND/contracts/reset.sql" && echo "[studio] 录音棚已复位"
  kill_port 8010
  echo "[studio] 录制完成（写操作用例的 SES/DB 残留请按需清理：模板 subject=语料模板）"
  ;;
diff)
  kill_port 8011 || true
  (cd "$BACKEND" && DATABASE_URL="$DBURL" SECRET_KEY="$SECRET" SERVER_PORT=8011 \
    ./bin/ses-sender.exe serve --mode=api > "$BACKEND/../.go-harness.log" 2>&1 &)
  wait_http http://localhost:8011/ 30
  cd "$BACKEND" && ./bin/harness.exe -mode diff -base http://localhost:8011 \
    -corpus contracts/corpus.yaml -golden contracts/golden
  rc=$?
  kill_port 8011
  exit $rc
  ;;
*)
  echo "用法: bash contracts/studio.sh record|diff|reset"; exit 1
  ;;
esac
