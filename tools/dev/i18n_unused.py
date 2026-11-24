"""找界面文案里没人用的条目、中英文不一致的条目。

用法：python tools/dev/i18n_unused.py          只列出来
      python tools/dev/i18n_unused.py --drop   把没人用的从 zh.ts、en.ts 里删掉

"没人用"的判断：键名（例如 'adm.title'）在 web/src 里除了文案文件以外一次都没出现过。
代码里拼出来的键（例如 t(`adm.role.${x}`)）按前缀算：只要出现了 `adm.role.` 这样的前缀，这一组都算有人用。
"""
import os
import re
import sys

ROOT = os.path.join(os.path.dirname(__file__), '..', '..', 'web', 'src')
I18N = os.path.join(ROOT, 'i18n')
KEY = re.compile(r"^  '([^']+)': ", re.M)


def keys(lang):
    return KEY.findall(open(os.path.join(I18N, lang + '.ts'), encoding='utf-8').read())


def code_text():
    out = []
    for d, _, fs in os.walk(ROOT):
        if os.path.abspath(d).startswith(os.path.abspath(I18N)):
            continue
        for f in fs:
            if f.endswith(('.ts', '.tsx')):
                out.append(open(os.path.join(d, f), encoding='utf-8').read())
    # glossary.ts 在 i18n 目录里，但它是代码不是文案
    g = os.path.join(I18N, 'glossary.ts')
    if os.path.exists(g):
        out.append(open(g, encoding='utf-8').read())
    return '\n'.join(out)


def main():
    zh, en = keys('zh'), keys('en')
    src = code_text()
    # 拼出来的键的前缀：`xxx.${...}` 或 'xxx.' + ...
    # 例如 `adm.haltLv${n}`、`mg.n${i}`、'adm.role.' + x：前缀不一定以点结尾
    prefixes = {p for p in re.findall(r"[`'\"]([a-zA-Z0-9_]+\.[a-zA-Z0-9_.]*)(?:\$\{|['\"]\s*\+)", src) if len(p) >= 3}
    unused = []
    for k in zh:
        if k in src:
            continue
        if any(k.startswith(p) for p in prefixes):
            continue
        unused.append(k)
    only_zh = sorted(set(zh) - set(en))
    only_en = sorted(set(en) - set(zh))
    print(f'中文 {len(zh)} 条，英文 {len(en)} 条；没人用的 {len(unused)} 条')
    for k in unused:
        print('  没人用：', k)
    for k in only_zh:
        print('  只有中文：', k)
    for k in only_en:
        print('  只有英文：', k)
    if '--drop' in sys.argv and unused:
        for lang in ('zh', 'en'):
            p = os.path.join(I18N, lang + '.ts')
            b = open(p, encoding='utf-8').read()
            for k in unused:
                b = re.sub(r"^  '" + re.escape(k) + r"': .*\n", '', b, flags=re.M)
            open(p, 'w', encoding='utf-8', newline='\n').write(b)
        print('已删掉')


main()
