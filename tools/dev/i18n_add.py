"""改 zh.ts / en.ts 的词条。用法：python tools/dev/i18n_add.py strings.py（strings.py 放在会话临时目录，不进仓库）
strings.py 里定义 ZH、EN 两个 dict（键要一样），可选 DROP：要删掉的键或前缀（以 * 结尾）。同名的词条覆盖。"""
import json
import os
import re
import sys

src = {}
exec(open(sys.argv[1], encoding='utf-8').read(), src)
ZH, EN, DROP = src.get('ZH', {}), src.get('EN', {}), src.get('DROP', [])
ALLOW = set(src.get('ALLOW', []))
assert set(ZH) == set(EN), set(ZH) ^ set(EN)
base = os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', '..', 'web', 'src', 'i18n') + os.sep


def drop_key(s, k):
    if k.endswith('*'):
        return re.sub(r"\n  '" + re.escape(k[:-1]) + r"[^']*': .*", '', s)
    return re.sub(r"\n  '" + re.escape(k) + r"': .*", '', s)


def put(path, d, end):
    s = open(path, encoding='utf-8', newline='').read()
    clash = [k for k in d if re.search(r"\n  '" + re.escape(k) + r"': ", s) and k not in ALLOW]
    if clash:
        sys.exit('key already exists (add to ALLOW to overwrite): ' + ', '.join(clash))
    for k in DROP:
        s = drop_key(s, k)
    lines = []
    for k, v in d.items():
        s = drop_key(s, k)
        lines.append(f"  '{k}': {json.dumps(v, ensure_ascii=False)},")
    if lines:
        i = s.rindex(end) if end else s.rstrip().rindex('}')
        s = s[:i] + '\n'.join(lines) + '\n' + s[i:]
    open(path, 'w', encoding='utf-8', newline='').write(s)


put(base + 'zh.ts', ZH, '} as const')
put(base + 'en.ts', EN, None)
print(len(ZH), 'keys set,', len(DROP), 'drop rules')
