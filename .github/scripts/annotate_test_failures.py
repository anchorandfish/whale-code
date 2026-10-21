#!/usr/bin/env python3
"""把 `go test` 输出里的失败块转成 GitHub Actions 注解（::error::）。

为什么需要这个脚本（2026-10-22 的教训）：
    Actions 的日志正文只有登录态能读 —— `GET /repos/{o}/{r}/actions/jobs/{id}/logs`
    匿名请求返回 403，仓库页的 HTML 也不含日志（前端异步拉取）。于是「CI 红了」
    对人以外的一切都只是一行 `FAIL <pkg> 37.492s`：既不知道哪个用例挂了，
    也不知道挂在哪一句断言上，排查只能靠人肉贴日志。

    check-run 的 **annotation 是匿名可读的**（`GET /check-runs/{id}/annotations`）。
    所以：在测试失败时，把每个失败块压成一条注解，抛给 Actions 呈现。

用法：
    python3 .github/scripts/annotate_test_failures.py /tmp/go-test.log

设计要点：
  - 只做"搬运"，不改判定：退出码仍由调用方（CI step）决定，本脚本永远 exit 0，
    否则会把"测试失败"变成"注解步骤失败"，掩盖真正的原因。
  - 注解正文里的 `%`、`\\r`、`\\n` 必须转义成 %25 / %0D / %0A，否则 Actions 只显示第一行。
  - 每个失败块截断到 20 行：注解有长度上限，超长会被整条丢弃（丢弃 = 又变成看不见）。
"""
from __future__ import annotations

import re
import sys

MAX_LINES_PER_BLOCK = 20

# `--- FAIL: TestX (0.00s)` / `    --- FAIL: TestX/sub (0.00s)`（子测试带缩进）
FAIL_HEAD = re.compile(r"^\s*---\s+FAIL:\s+(\S+)")
# 包级收尾：`FAIL\tgithub.com/x/y\t1.234s` 或 `FAIL\tgithub.com/x/y [build failed]`
PKG_FAIL = re.compile(r"^FAIL\s+(\S+)")
# 非断言类失败：panic / 数据竞争 / 超时
CRASH_HEAD = re.compile(r"^(panic:|WARNING: DATA RACE|fatal error:)")
# 块的结束信号：下一个用例开始、包级 FAIL、或测试二进制结束
BLOCK_END = re.compile(r"^(\s*---\s+(?:PASS|FAIL|SKIP):|=== RUN|FAIL\s|PASS$|ok\s)")


def escape(text: str) -> str:
    """GitHub annotation 的属性转义（顺序敏感：% 必须最先）。"""
    return (
        text.replace("%", "%25")
        .replace("\r", "%0D")
        .replace("\n", "%0A")
    )


def emit(message: str, title: str) -> None:
    print(f"::error title={escape(title)}::{escape(message)}")


def main(path: str) -> int:
    try:
        with open(path, encoding="utf-8", errors="replace") as fh:
            lines = fh.read().splitlines()
    except OSError as exc:  # 日志没写出来时不要吞掉：说明 tee 那条路有问题
        print(f"::warning::读不到测试日志 {path}: {exc}")
        return 0

    failures: list[str] = []  # 收集用例名，最后再发一条"汇总"注解
    i = 0
    while i < len(lines):
        line = lines[i]
        head = FAIL_HEAD.match(line)
        crash = CRASH_HEAD.match(line)
        if not (head or crash):
            i += 1
            continue

        if head:
            name = head.group(1)
            failures.append(name)
            title = f"FAIL {name}"
            # ★ 诊断行在 `--- FAIL` **之前**：Go 先打印 t.Errorf 的输出（缩进行），
            #   最后才打 `--- FAIL: Name (0.00s)`。所以往里**回溯**收集，而不是往后。
            #   （第一版就是往后取的：结果注解里只有一行 `--- FAIL`，诊断全丢 —— 用构造
            #   日志跑判别时才发现。）
            body: list[str] = []
            j = i - 1
            while j >= 0:
                prev = lines[j]
                if BLOCK_END.match(prev):
                    break  # 上一条用例的开始/结束
                if prev.strip() and not prev.startswith((" ", "\t")):
                    break  # 非缩进行：属于包级输出（panic/FAIL 行），不属于本条用例
                body.append(prev.rstrip())
                j -= 1
            body.reverse()
            body.append(line.strip())
            # 去掉开头/结尾的空行，避免注解里挂一串空白
            while body and not body[0].strip():
                body.pop(0)
            while len(body) > 1 and not body[-1].strip():
                body.pop()
            i += 1
        else:
            title = line.strip().split(":")[0]
            body = [line.rstrip()]
            j = i + 1
            while j < len(lines) and not (FAIL_HEAD.match(lines[j]) or PKG_FAIL.match(lines[j])):
                body.append(lines[j].rstrip())
                if len(body) >= 60:  # panic/race 的栈很长，留够定位信息即可
                    body.append("…（栈已截断，完整内容见 Actions 日志）")
                    break
                j += 1
            i = j

        if len(body) > MAX_LINES_PER_BLOCK:
            omitted = len(body) - MAX_LINES_PER_BLOCK
            body = body[:MAX_LINES_PER_BLOCK] + [f"…（其余 {omitted} 行见 Actions 日志）"]
        emit("\n".join(body), title)

    # 包级 FAIL 也发一条：即使所有用例块都被截断，至少能看出是哪几个包红的
    pkg_failures = [m.group(1) for m in (PKG_FAIL.match(x) for x in lines) if m]
    if pkg_failures:
        emit("失败的包：" + ", ".join(dict.fromkeys(pkg_failures)), "go test 汇总")
    if failures:
        emit(f"失败的用例（{len(failures)} 个）：\n" + "\n".join(dict.fromkeys(failures)), "失败用例清单")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else "/tmp/go-test.log"))
