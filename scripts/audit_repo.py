#!/usr/bin/env python3
"""Dog-dev 仓库门禁：脱敏扫描 + 元信息一致性 + 脚本语法 + 打包树洁净。

用法: python3 scripts/audit_repo.py [--root .]
无第三方依赖；命中即 exit 1。CI 与本地跑同一份。
只扫描 git 跟踪文件，保证本地与 CI（干净 checkout）行为一致。
"""
from __future__ import annotations
import argparse, json, os, re, subprocess, sys
from pathlib import Path

# 放行：RFC5737 文档地址、环回。测试夹具内私网 IP 与 CIDR 网段按规则豁免。
IP_ALLOW = re.compile(rb"192\.0\.2\.|127\.0\.0\.1|0\.0\.0\.0|172\.17\.0\.1")
PATTERNS = [
    ("PRIVATE_IP", re.compile(rb"(?<![\d.])(?:192\.168\.\d{1,3}\.\d{1,3}|10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3})(?![\d.])")),
    ("HOST_PW",    re.compile(rb"(?<![A-Za-z])[A-Z][a-z]{2}[0-9]{6}(?![A-Za-z])")),
    ("GH_TOKEN",   re.compile(rb"gh[pousr]_[A-Za-z0-9]{16,}|github_pat" + rb"_[A-Za-z0-9_]{20,}")),
    ("CF_TOKEN",   re.compile(rb"cfut_[A-Za-z0-9_-]{20,}")),
    ("SK_KEY",     re.compile(rb"sk-[A-Za-z0-9]{20,}")),
    ("PERSONAL",   re.compile(rb"(?<![A-Za-z0-9])[0-9]{6}\.xyz|[\w-]+\.(?:dpdns|duckdns)\.org")),
    ("ABS_PATH",   re.compile(rb"/vol1/@app(?:share|data|conf|home)/com\.[\w.-]+")),
    ("SSH_CREDS",  re.compile(rb"sshpass|UserKnownHostsFile|StrictHostKeyChecking")),
    ("PEM",        re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----[\s\S]{0,40}?[A-Za-z0-9+/]{40,}={0,2}")),
    ("PW_ASSIGN",  re.compile(rb"(?i)(?:password|passwd|client_secret)\s*[:=]\s*['\"]?[A-Za-z0-9!@#_\-]{8,}")),
]
SKIP_DIRS = {".git", "__pycache__", "dist", "node_modules", ".build", ".vite"}
SKIP_FILES = re.compile(r"\.(fpk|pyc|png|jpg|jpeg|gif|ico|img|zip|tgz|gz|woff2?|map)$")
SELF = {"scripts/audit_repo.py", "scripts/ci_gate.py"}
# 项目公开的默认控制台口令/测试夹具值，不算私密凭据；真实用户口令不在此列。
DEFAULT_PW = re.compile(rb"admin123|legacy-pw")

def tracked_files(root):
    try:
        r = subprocess.run(["git", "-c", "safe.directory=*", "ls-files", "-z"],
                           cwd=root, capture_output=True)
        if r.returncode == 0:
            return [x.decode("utf-8", "replace") for x in r.stdout.split(b"\0") if x]
    except FileNotFoundError:
        pass
    out = []
    for dp, dns, fns in os.walk(root):
        dns[:] = [d for d in dns if d not in SKIP_DIRS]
        for fn in fns:
            out.append((Path(dp) / fn).relative_to(root).as_posix())
    return out

def scan_tree(root):
    hits, n = [], 0
    for rel in tracked_files(root):
        if SKIP_FILES.search(rel) or rel in SELF or "/node_modules/" in rel:
            continue
        p = root / rel
        if not p.is_file():
            continue
        try:
            data = p.read_bytes()
        except OSError:
            continue
        n += 1
        for name, pat in PATTERNS:
            for m in pat.finditer(data):
                s = m.group()
                if name == "PRIVATE_IP":
                    if IP_ALLOW.search(s):
                        continue
                    if data[m.end():m.end()+1] == b"/":   # CIDR 网段
                        continue
                    if rel.endswith("_test.go"):          # 测试夹具
                        continue
                if name == "PW_ASSIGN":
                    if DEFAULT_PW.search(data[m.start():m.end()+24]):
                        continue
                if name == "HOST_PW" and rel.endswith("_test.go"):
                    continue
                hits.append((rel, name, s[:60].decode("utf-8", "replace")))
    return n, hits

def check_meta(root):
    errs = []
    man = (root / "fpk/manifest").read_text(encoding="utf-8")
    mv = re.search(r"^version\s*=\s*(\S+)", man, re.M)
    if not mv:
        return ["manifest 无 version"], None
    v = mv.group(1)
    for rel in ["fpk/manifest", "fpk/wizard/install", "fpk/wizard/uninstall",
                "fpk/ICON.PNG", "fpk/ICON_256.PNG",
                "fpk/app/ui/images/icon-0.png", "fpk/app/ui/images/icon-64.png",
                "fpk/app/ui/images/icon-256.png", "fpk/app/ui/config",
                "fpk/config/privilege", "fpk/config/resource"]:
        if not (root / rel).exists():
            errs.append("缺 %s" % rel)
    for cb in ["install_init", "install_callback", "config_init", "config_callback",
               "upgrade_init", "upgrade_callback", "uninstall_init", "uninstall_callback"]:
        if not (root / "fpk/cmd" / cb).exists():
            errs.append("缺 fpk/cmd/%s" % cb)
    rd = (root / "README.md").read_text(encoding="utf-8")
    if v not in rd:
        errs.append("README 未提及当前版本 %s" % v)
    return errs, v

def check_scripts(root):
    errs = []
    for p in sorted((root / "fpk/cmd").glob("*")):
        if p.is_file():
            r = subprocess.run(["bash", "-n", str(p)], capture_output=True, text=True)
            if r.returncode:
                errs.append("%s bash -n: %s" % (p.name, r.stderr.strip()[:100]))
    for p in sorted((root / "fpk/wizard").glob("*")):
        if p.is_file():
            try:
                json.loads(p.read_text(encoding="utf-8"))
            except Exception as e:
                errs.append("%s JSON: %s" % (p.name, str(e)[:100]))
    for p in sorted((root / "scripts").glob("*.py")):
        if p.name in SELF:
            continue
        try:
            compile(p.read_text(encoding="utf-8"), str(p), "exec")
        except SyntaxError as e:
            errs.append("%s 语法错: %s" % (p.name, e))
    return errs

def check_clean(root):
    out = []
    for pat in ("__pycache__", "*.pyc"):
        out += [str(p.relative_to(root)) for p in root.rglob(pat)
                if ".git" not in p.parts and "node_modules" not in p.parts]
    return out

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=".")
    root = Path(ap.parse_args().root).resolve()
    print("审计根:", root)
    n, hits = scan_tree(root)
    print("扫描入库文本文件 %d，敏感命中 %d" % (n, len(hits)))
    for h in hits[:40]:
        print("  HIT %-46s %-11s %s" % (h[0][:46], h[1], h[2]))
    errs = ["脱敏命中: %s %s" % (h[0], h[1]) for h in hits]
    e, v = check_meta(root); errs += e
    errs += check_scripts(root)
    errs += ["残留: %s" % p for p in check_clean(root)[:10]]
    print("manifest 版本:", v)
    if errs:
        print("\n===== AUDIT FAILED (%d) =====" % len(errs))
        for x in errs[:50]:
            print("  -", x)
        sys.exit(1)
    print("\n===== AUDIT PASS: 脱敏 / 元信息 / 脚本语法 / 打包树 全绿 =====")

if __name__ == "__main__":
    main()
