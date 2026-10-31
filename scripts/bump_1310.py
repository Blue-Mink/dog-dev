#!/usr/bin/env python3
"""把 FPK manifest 从 1.3.8 升到 1.3.10（补记 1.3.9 与 1.3.10 两条 changelog）。带断言，重复执行会拒绝。"""
import re
import sys

V139 = (
    "1.3.9 修复 DNS 优选把「握手能通、HTTP 不通」的死节点写进 hosts、把整机 github.com 打进黑洞："
    "①写盘前对前 3 个候选各做 2 次 HTTP 校验（8s），不过就跳过该域名；"
    "②优选全部失败、或决策判定两条加速通路全灭时，自动清理 hosts 托管区，不再保留上一轮的旧值；"
    "③候选池新增 Cloudflare / Google / Quad9 三个海外 DoH，并给 github / api / codeload / raw / objects "
    "各补 GitHub 官方 IP 段兜底（国内 DoH 只回一个 CN 入口时仍有备选）。"
)
V1310 = (
    "1.3.10 加速源清单扩充：把 Moo 加速源清单与 FnDepot 下发清单里 dog-dev 没有的源逐个实测，"
    "并入两个可用项（github.tbap.top，以及仅 HTTP 可用的 fndepot1），老配置升级时按 URL 去重自动补齐；"
    "控制台加速源列表对非加密 http 源显示琥珀色感叹号提示（明文传输、下载内容无签名兜底，请核对 sha256）。"
)

path = sys.argv[1] if len(sys.argv) > 1 else "fpk/manifest"
s = open(path, encoding="utf-8").read()

if "version=1.3.10" in s:
    sys.exit(f"{path}: 已经是 1.3.10，跳过")

assert s.count("version=1.3.8\n") == 1, f"{path}: 未找到唯一的 version=1.3.8 行"
s = s.replace("version=1.3.8\n", "version=1.3.10\n", 1)

m = re.search(r"^changelog=(.*)$", s, re.M)
assert m, f"{path}: 未找到 changelog 行"
assert not m.group(1).startswith("1.3.9"), f"{path}: changelog 已含 1.3.9"
s = s[: m.start(1)] + V1310 + " " + V139 + " " + m.group(1) + s[m.end(1):]

open(path, "w", encoding="utf-8").write(s)
print(f"{path}: 升版 1.3.10 完成，changelog 前置 {len(V1310) + len(V139) + 2} 字符")
