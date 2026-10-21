#!/usr/bin/env python3
"""最小 MCP stdio server —— S6/G8 端到端「帧格式」判别夹具。

独立按 MCP spec 2024-11-05 §Transports/stdio 实现（newline-delimited JSON），
**刻意不参考 whale 的 transport_stdio.go** —— 否则退化成「自证式 mock」，
对线协议正确性零判别力（见 G-REBUILD_STATUS.md §3.1 ③④）。

用法：
    wire_server.py                    合规：单行 JSON + '\n'（读/写都合规）
    wire_server.py --legacy-content-length
                                      整机不合规：读/写都用 LSP 风格 Content-Length 头帧。
                                      负向反例 A：判别「写侧是否仍是 newline」
                                      —— 若 whale 被改回写 CL 帧，本 server 会正常应答 ⇒ 反例变红。
    wire_server.py --legacy-write     只把**写侧**改成不合规 CL 头帧（读侧仍收 newline）。
                                      负向反例 B：判别「读侧是否做了双格式兼容」
                                      —— 若 whale 读侧同时接受 CL，本 server 会被接入 ⇒ 反例变红。
                                      CL 帧按 LSP 真实形态写：body 后**不加** '\n'，
                                      因此 newline-only 读侧会一直等不到分隔符（确定性超时）。

退出码：EOF 正常退出 0；读到无法解析的帧（协议不符）退出 3（把失败变确定性，而不是挂死）。
"""
import json
import sys

ARGS = sys.argv[1:]
READ_LEGACY = ("--legacy-content-length" in ARGS) or ("--legacy-read" in ARGS)
WRITE_LEGACY = ("--legacy-content-length" in ARGS) or ("--legacy-write" in ARGS)

BIN = sys.stdin.buffer
BOUT = sys.stdout.buffer


class EOF(Exception):
    pass


class ProtocolMismatch(Exception):
    """对端用了与本 server 不同的帧格式（确定性失败，而不是挂死）。"""


def read_msg():
    """读一帧；空行返回 None。"""
    if READ_LEGACY:
        header = BIN.readline()
        if not header:
            raise EOF
        if not header.startswith(b"Content-Length:"):
            raise ProtocolMismatch(
                "legacy-read 期望 'Content-Length:' 头，收到 %r" % header[:120]
            )
        try:
            n = int(header.split(b":", 1)[1].strip())
        except ValueError as exc:
            raise ProtocolMismatch("Content-Length 数值非法: %s" % exc)
        if BIN.read(2) != b"\r\n":
            raise ProtocolMismatch("Content-Length 头后缺少 CRLF")
        return json.loads(BIN.read(n))

    line = BIN.readline()
    if not line:
        raise EOF
    line = line.strip()
    if not line:
        return None
    return json.loads(line)


def write_msg(obj):
    body = json.dumps(obj, separators=(",", ":")).encode("utf-8")
    if WRITE_LEGACY:
        # LSP 真实形态：body 之后没有 '\n'
        BOUT.write(b"Content-Length: %d\r\n\r\n" % len(body))
        BOUT.write(body)
    else:
        BOUT.write(body)
        BOUT.write(b"\n")
    BOUT.flush()


def main():
    while True:
        try:
            req = read_msg()
        except EOF:
            return 0
        except ProtocolMismatch as exc:
            sys.stderr.write("wire_server: 协议不符: %s\n" % exc)
            return 3
        except Exception as exc:  # noqa: BLE001
            sys.stderr.write("wire_server: 帧解析失败: %s\n" % exc)
            return 1
        if req is None:
            continue
        if not isinstance(req, dict):
            continue

        method = req.get("method")
        req_id = req.get("id")

        if method == "initialize":
            write_msg({
                "jsonrpc": "2.0", "id": req_id,
                "result": {
                    "protocolVersion": "2025-06-18",
                    "capabilities": {"tools": {}},
                    "serverInfo": {"name": "s6-wire-probe", "version": "1.0.0"},
                },
            })
        elif method == "notifications/initialized":
            pass  # 通知不响应
        elif method == "ping":
            write_msg({"jsonrpc": "2.0", "id": req_id, "result": {}})
        elif method == "tools/list":
            write_msg({
                "jsonrpc": "2.0", "id": req_id,
                "result": {"tools": [{
                    "name": "echo",
                    "description": "Echo back the provided text (S6 wire probe)",
                    "inputSchema": {
                        "type": "object",
                        "properties": {"text": {"type": "string"}},
                        "required": ["text"],
                    },
                }]},
            })
        elif method == "tools/call":
            params = req.get("params") or {}
            args = params.get("arguments") or {}
            text = str(args.get("text", ""))
            write_msg({
                "jsonrpc": "2.0", "id": req_id,
                "result": {
                    "content": [{"type": "text", "text": "echo:" + text}],
                    "isError": False,
                },
            })
        elif req_id is not None:
            write_msg({
                "jsonrpc": "2.0", "id": req_id,
                "error": {"code": -32601, "message": "method not found: %s" % method},
            })
        # 其余通知：忽略


if __name__ == "__main__":
    sys.exit(main() or 0)
