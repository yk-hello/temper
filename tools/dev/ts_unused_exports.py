"""找前端里导出了、但别的文件都没用到的名字（函数、常量、类型）。只列出来，删不删人看。

用法：python tools/dev/ts_unused_exports.py
"""
import os
import re

ROOT = os.path.join(os.path.dirname(__file__), '..', '..', 'web', 'src')
EXP = re.compile(r'^export\s+(?:async\s+)?(?:function\*?|const|let|class|interface|type|enum)\s+([A-Za-z_$][\w$]*)', re.M)

files = {}
for d, _, fs in os.walk(ROOT):
    for f in fs:
        if f.endswith(('.ts', '.tsx')):
            p = os.path.join(d, f)
            files[p] = open(p, encoding='utf-8').read()

for p, src in sorted(files.items()):
    for name in EXP.findall(src):
        word = re.compile(r'(?<![\w$])' + re.escape(name) + r'(?![\w$])')
        elsewhere = any(word.search(s) for q, s in files.items() if q != p)
        if elsewhere:
            continue
        inside = len(word.findall(src))
        rel = os.path.relpath(p, ROOT).replace('\\', '/')
        print(f'{rel}: {name}' + ('（本文件里也没用到）' if inside <= 1 else '（只在本文件里用：可以去掉 export）'))
