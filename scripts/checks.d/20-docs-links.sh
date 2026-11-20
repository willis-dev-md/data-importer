#!/usr/bin/env bash
# Every relative markdown link in README.md, CLAUDE.md and docs/ must resolve to a file.
cd "$(dirname "$0")/../.." || exit 1
python3 -I - <<'PY'
import os, re, sys
bad = 0
files = ["README.md", "CLAUDE.md"] + [os.path.join(d, f) for d, _, fs in os.walk("docs") for f in fs if f.endswith(".md")]
for f in files:
    if not os.path.exists(f): continue
    base = os.path.dirname(f)
    for m in re.finditer(r"\]\(([^)\s]+)\)", open(f, encoding="utf-8").read()):
        t = m.group(1).split("#")[0]
        if not t or re.match(r"^(https?:|mailto:)", t): continue
        if not os.path.exists(os.path.normpath(os.path.join(base, t))):
            print(f"FAIL: {f}: broken link {m.group(1)}"); bad = 1
sys.exit(bad)
PY
