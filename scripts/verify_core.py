#!/usr/bin/env python3
"""ShieldDNS 核心自检脚本：直接向本地 DNS 端口发查询，验证拦截/放行/关闭保护。"""
import json
import socket
import struct
import sys
import urllib.request

DNS_PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 15353
CONTROL = f"http://127.0.0.1:{sys.argv[2] if len(sys.argv) > 2 else 5399}"

RCODE = {0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL", 3: "NXDOMAIN", 5: "REFUSED"}


def encode_name(name):
    out = b""
    for label in name.rstrip(".").split("."):
        out += bytes([len(label)]) + label.encode()
    return out + b"\x00"


def query(name, qtype=1, timeout=5):
    txid = 0x4A21
    msg = struct.pack(">HHHHHH", txid, 0x0100, 1, 0, 0, 0)
    msg += encode_name(name) + struct.pack(">HH", qtype, 1)
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(timeout)
    try:
        sock.sendto(msg, ("127.0.0.1", DNS_PORT))
        data, _ = sock.recvfrom(8192)
    finally:
        sock.close()
    rid, flags, qd, an, ns, ar = struct.unpack(">HHHHHH", data[:12])
    return rid, RCODE.get(flags & 0xF, str(flags & 0xF)), an


def api(path, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(CONTROL + path, data=data, method="POST" if data else "GET")
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode())


def main():
    blocked = "1rtb.com"
    allowed = "www.baidu.com"

    print("== 保护开启 ==")
    for name in (blocked, allowed):
        rid, rcode, an = query(name)
        print(f"  {name:22s} -> {rcode:8s} answers={an} id={rid:#06x}")

    status = api("/api/status")
    print(f"  查询总数={status['queries']['total']} 拦截={status['queries']['blocked']} 规则={status['rules_total']}")

    print("== 关闭保护 ==")
    api("/api/protection", {"enabled": False})
    rid, rcode, an = query(blocked)
    print(f"  {blocked:22s} -> {rcode:8s} answers={an} (关闭后应放行)")

    print("== 重新开启保护 ==")
    api("/api/protection", {"enabled": True})
    rid, rcode, an = query(blocked)
    print(f"  {blocked:22s} -> {rcode:8s} answers={an}")

    logs = api("/api/logs?limit=6")
    print("== 最近日志 ==")
    for item in logs["logs"]:
        print(f"  {item['time']} {item['domain']:24s} {item['action']:8s} {item['rule'][:40]}")


if __name__ == "__main__":
    main()
