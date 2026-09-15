#!/usr/bin/env python3
"""任务定义构建器（"填表员"）：模板 + 参数 → 最终的 ECS 任务定义 JSON。

干什么：
    taskdef-template.json 是一张带 {{空位}} 的任务定义表格；
    本脚本把 deploy.sh 查到的值（镜像地址/角色/安全组/密钥路径等）填进空位，
    并做两道防呆校验：① 有空位没填到 → 拒绝输出（防止残缺配置上线）
                      ② 填完的结果必须是合法 JSON（防止模板被改坏）

用法（一般由 deploy.sh 调用，也可手动测试）：
    python taskdef.py --template taskdef-template.json \
        --image 303235427801.dkr.ecr.us-east-2.amazonaws.com/gs/ses-sender/backend:v0.1.0 \
        --set FAMILY=ses-sender-backend-prod \
        --set EXECUTION_ROLE_ARN=arn:... --set TASK_ROLE_ARN=arn:... \
        [--output taskdef.json]

特点：纯 Python 标准库、无第三方依赖、Windows/Linux 通用。
"""
import argparse
import json
import os
import re
import sys


def load_env_file(path: str) -> dict:
    """可选功能：解析 KEY=VALUE 格式的参数文件（# 开头为注释行）。

    当前部署链路已改为 --set 传参，此函数保留作为兼容入口。
    """
    values = {}
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue                      # 跳过空行和注释
            if "=" not in line:
                continue                      # 跳过不是 KEY=VALUE 的行
            key, _, val = line.partition("=")
            values[key.strip()] = val.strip()
    return values


def main() -> int:
    # ── 解析命令行参数 ──
    parser = argparse.ArgumentParser(description="构建 ECS 任务定义（模板填空+校验）")
    parser.add_argument("--template", required=True, help="任务定义模板 JSON 路径")
    parser.add_argument("--env-file", default=None, help="（可选）KEY=VALUE 参数文件")
    parser.add_argument("--image", required=True, help="完整镜像 URI（含版本 tag）")
    parser.add_argument("--set", action="append", default=[],
                        help="填入模板的参数 KEY=VALUE，可重复多次")
    parser.add_argument("--output", default="-", help="输出文件路径，默认打印到屏幕")
    args = parser.parse_args()

    # ── 第 1 步：汇总所有参数值（参数池）──
    values = load_env_file(args.env_file) if args.env_file else {}
    values["IMAGE"] = args.image              # 镜像地址总是来自 --image（版本号本体）
    for kv in args.set:
        key, _, val = kv.partition("=")
        if not key:
            parser.error(f"--set 参数格式错误: {kv}")
        values[key] = val

    # ── 第 2 步：读模板 ──
    with open(args.template, encoding="utf-8") as f:
        text = f.read()

    # ── 第 3 步：替换所有 {{占位符}} ──
    # 找到没赋值的、或值还是 TODO 开头的 → 记下来（最后统一报错，一次性列全）
    unresolved = []

    def substitute(match: "re.Match") -> str:
        key = match.group(1)
        if key not in values:                 # 情况1：压根没人传这个值
            unresolved.append(key)
            return match.group(0)
        val = values[key]
        if val.startswith("TODO"):            # 情况2：传了但还是占位值 TODO
            unresolved.append(key)
        return val

    rendered = re.sub(r"\{\{(\w+)\}\}", substitute, text)

    # 防呆①：有任何空位没填 → 列出全部缺失项并退出，禁止残缺配置进入注册环节
    if unresolved:
        print(f"[taskdef] 错误：以下占位符未赋值或仍为 TODO，禁止部署：{sorted(set(unresolved))}",
              file=sys.stderr)
        return 1

    # 防呆②：填完必须仍是合法 JSON（模板被改坏、值里带了引号等都会在这暴露）
    try:
        data = json.loads(rendered)
    except json.JSONDecodeError as e:
        print(f"[taskdef] 错误：渲染结果不是合法 JSON：{e}", file=sys.stderr)
        return 1

    # ── 第 4 步：输出（写文件或打印）──
    out = json.dumps(data, ensure_ascii=False, indent=2)
    if args.output == "-":
        print(out)
    else:
        os.makedirs(os.path.dirname(os.path.abspath(args.output)), exist_ok=True)
        with open(args.output, "w", encoding="utf-8", newline="\n") as f:
            f.write(out + "\n")
        print(f"[taskdef] 已生成 {args.output}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
