"""查密钥有没有残留。只打印变量名和位置，绝不打印密钥的值。

    python tools/dev/secrets_check.py [要一起查的目录，比如开源导出的目录]

1. 本机 .env / deploy/.env 里每个够长的值：在已跟踪的文件、全部提交历史、给的目录里做精确查找。
2. 常见密钥的样式（sk-、Telegram bot token、GitHub token、AWS、私钥块、64 位的币安密钥、写死的主密钥、带密码的网址）：
   在已跟踪的文件和全部提交历史里扫。
有残留退出码 1。模型名、地址这类不是密钥的值不算。
"""
import os
import re
import subprocess
import sys

sys.stdout.reconfigure(encoding="utf-8")
ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
EXTRA = sys.argv[1] if len(sys.argv) > 1 else ""
os.chdir(ROOT)

NOT_SECRET = {"AI_MODEL", "DEEPSEEK_MODEL", "MARKET_SOURCE", "BRAND_NAME", "SMTP_HOST", "SMTP_USER", "MAIL_FROM", "MAIL_FROM_NAME",
              "SITE_DOMAIN", "ALLOWED_ORIGINS", "QS_SIGNUP", "AI_BASE_URL", "SMTP_PORT", "QS_PORT", "QS_BIND",
              "NEWS_FEEDS", "X_ACCOUNTS", "X_API_BASE"}


def git(*a):
    return subprocess.run(["git", "-c", "core.quotepath=off", *a], capture_output=True, text=True, encoding="utf-8", errors="replace").stdout


def env_values():
    out = {}
    for envf in ("deploy/.env", ".env"):
        if not os.path.exists(envf):
            continue
        for line in open(envf, encoding="utf-8", errors="replace"):
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, v = line.split("=", 1)
            k, v = k.strip(), v.strip().strip('"').strip("'")
            if k == "QSIG_KEYS":  # id:secret:strategy;... 每段的 secret 单独查
                for i, part in enumerate(v.split(";")):
                    seg = part.split(":")
                    if len(seg) >= 2 and len(seg[1]) >= 12:
                        out[f"{envf}:QSIG_KEYS[{i}].secret"] = seg[1]
                continue
            if k in NOT_SECRET or len(v) < 12:
                continue
            out[f"{envf}:{k}"] = v
    return out


def walk(d):
    for root, _, files in os.walk(d):
        if "node_modules" in root or ".git" in root.split(os.sep):
            continue
        for f in files:
            yield os.path.join(root, f)


bad = 0
vals = env_values()
print(f"本机 .env 里要查的值：{len(vals)} 个（{', '.join(sorted(vals)) or '没有 .env'}）")
if git("ls-files", "deploy/.env", ".env").strip():
    bad += 1
    print("!! .env 被 git 跟踪了")
if git("log", "--all", "--oneline", "--", "deploy/.env", ".env").strip():
    bad += 1
    print("!! 提交历史里出现过 .env 文件")
for name, val in vals.items():
    hit = git("grep", "-l", "-F", "-e", val).strip()
    if hit:
        bad += 1
        print(f"!! {name} 出现在已跟踪的文件里：{hit.splitlines()}")
    h = git("log", "--all", "--oneline", "-S", val).strip()
    if h:
        bad += 1
        print(f"!! {name} 出现在提交历史里：{[l.split()[0] for l in h.splitlines()]}")
    if EXTRA and os.path.isdir(EXTRA):
        for fp in walk(EXTRA):
            try:
                if val in open(fp, encoding="utf-8", errors="ignore").read():
                    bad += 1
                    print(f"!! {name} 出现在 {os.path.relpath(fp, EXTRA)}")
            except OSError:
                pass

PATTERNS = {
    "sk- 开头的接口密钥": r"sk-[A-Za-z0-9_\-]{20,}",
    "Telegram bot token": r"\b\d{8,10}:AA[A-Za-z0-9_\-]{30,}",
    "GitHub token": r"\b(ghp|gho|ghs|ghu)_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}",
    "AWS key": r"\bAKIA[0-9A-Z]{16}\b",
    "私钥块": r"BEGIN [A-Z ]*PRIVATE KEY",
    "64 位字母数字（币安密钥的样子）": r"(?<![A-Za-z0-9])[A-Za-z0-9]{64}(?![A-Za-z0-9])",
    "写死的主密钥": r"QS_MASTER_KEY\s*[=:]\s*[\"']?[A-Za-z0-9+/]{43}=",
    "带密码的网址": r"https?://[^/\s:@]+:[^/\s:@]{6,}@",
}
# 已知不是密钥的：测试用的假值、哈希、示例
SAFE = re.compile(r"e2efake|fakekey|fakesecret|test-token|test-deepseek|example|0123456789abcdef|sha256|chain", re.I)
SKIP = ("package-lock.json", "go.sum", ".png", ".jpg", ".gif", ".ico", ".woff2")


def suspicious(name, s, ctx):
    if name.startswith("64 位") and (re.fullmatch(r"[0-9a-fA-F]{64}", s) or s.isdigit() or s.isalpha()):
        return False  # 十六进制哈希、纯数字、纯字母
    return name.startswith("写死") or not SAFE.search(ctx)


for f in git("ls-files").splitlines():
    if f.endswith(SKIP) or f.endswith("secrets_check.py") or not os.path.isfile(f):
        continue
    text = open(f, encoding="utf-8", errors="ignore").read()
    for name, pat in PATTERNS.items():
        for m in re.finditer(pat, text):
            if suspicious(name, m.group(0), text[max(0, m.start() - 60):m.end() + 20]):
                bad += 1
                print(f"!! 已跟踪的文件：{name} @ {f}（长度 {len(m.group(0))}）")

commit, cur, seen = "", "", set()
for line in git("log", "--all", "-p", "--no-color", "--unified=0").splitlines():
    if line.startswith("commit "):
        commit = line[7:15]
    elif line.startswith("+++ b/"):
        cur = line[6:]
    elif line.startswith("+") and not line.startswith("+++") and not cur.endswith(SKIP) and not cur.endswith("secrets_check.py"):
        for name, pat in PATTERNS.items():
            for m in re.finditer(pat, line):
                key = (name, cur, m.group(0)[:6])
                if key not in seen and suspicious(name, m.group(0), line):
                    seen.add(key)
                    bad += 1
                    print(f"!! 提交历史：{name} @ {commit} {cur}（长度 {len(m.group(0))}）")

if re.search(r"https?://[^/\s]+:[^/\s]+@|https?://[A-Za-z0-9_]{20,}@", git("remote", "-v")):
    bad += 1
    print("!! 远程仓库地址里带着令牌")

print("没有密钥残留" if not bad else f"有 {bad} 处要处理")
sys.exit(1 if bad else 0)
