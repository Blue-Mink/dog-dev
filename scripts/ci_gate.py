#!/usr/bin/env python3
"""CI 门禁总入口：脱敏 → go vet/test → 前端语法 → 元信息 → 汇总。

用法: python3 scripts/ci_gate.py [--root .] [--skip-go] [--skip-fpk]
CI workflow 与本地跑同一份，避免"本地绿线上红"。
"""
from __future__ import annotations
import argparse, os, re, subprocess, sys
from pathlib import Path

class _Missing:
    returncode = 127
    stdout = ""
    stderr = "command not found"

def run(cmd, cwd=None, env=None):
    print("  $", " ".join(str(c) for c in cmd))
    try:
        r = subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True)
    except FileNotFoundError as ex:
        print("    command not found:", ex)
        return _Missing()
    if r.returncode:
        print("    rc=%d\n    stdout=%s\n    stderr=%s" % (r.returncode, r.stdout[-400:], r.stderr[-600:]))
    return r

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=".")
    ap.add_argument("--skip-go", action="store_true", help="跳过 go vet/test")
    a = ap.parse_args()
    root = Path(a.root).resolve()
    fails = []

    print("[1/4] 脱敏 + 元信息 + 脚本语法门禁")
    r = run([sys.executable, "scripts/audit_repo.py", "--root", str(root)], cwd=root)
    if r.returncode: fails.append("audit_repo.py")

    print("[2/4] Go 静态检查与单测")
    if a.skip_go:
        print("  SKIP (--skip-go)")
    else:
        env = dict(os.environ, HOME="/tmp", GOPATH="/tmp/gopath",
                   GOMODCACHE="/tmp/gomod", GOCACHE="/tmp/gocache",
                   GOPROXY="https://goproxy.cn,direct")
        for c in (["go", "vet", "./..."], ["go", "test", "./..."]):
            r = run(c, cwd=root, env=env)
            if r.returncode: fails.append(" ".join(c))
        r = run(["gofmt", "-l", "."], cwd=root)
        if r.returncode == 0 and getattr(r, "stdout", "").strip():
            fails.append("gofmt 未格式化: %s" % r.stdout.strip()[:200])

    print("[3/4] 前端 JS 语法 + manifest 元信息")
    node = os.environ.get("NODE_BIN", "node")
    for p in [root/"web/ui/src/main.js", root/"web/ui/src/api.js", root/"web/ui/src/base.js",
              root/"web/ui/src/App.vue"]:
        if p.exists() and p.suffix == ".js":
            r = run([node, "--check", str(p)])
            if r.returncode: fails.append("node --check %s" % p.name)
    css = root/"web/ui/src/style.css"
    if css.exists():
        c = css.read_text(encoding="utf-8")
        if c.count("{") != c.count("}"):
            fails.append("style.css 花括号不配平 %d/%d" % (c.count("{"), c.count("}")))

    print("[4/4] 汇总")
    if fails:
        print("\n===== CI GATE FAILED =====")
        for f in fails: print("  -", f)
        sys.exit(1)
    print("\n===== CI GATE PASS =====")

if __name__ == "__main__":
    main()
