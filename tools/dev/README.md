# 开发脚本

在 Windows 的 Git Bash 里跑（Claude Code 的 Bash 工具就是它）。测试环境只用 :18000（后端）、:4173（前端）、`qs-test-pg`（数据库，127.0.0.1:55433），**不碰用户正在用的 8080 和 `qs-db`**。

工作副本放在固定目录 `%LOCALAPPDATA%\qs-dev`（可用环境变量 `QS_DEV_DIR` 改），不在仓库里。回归跑着的时候可以继续改仓库：脚本先把源码同步过去再跑。测试用的主密钥和 qsig 密钥第一次运行时随机生成，存在工作目录里，不打印、不进仓库。

| 脚本 | 做什么 |
| --- | --- |
| `bash tools/dev/env_up.sh [fresh]` | 起或更新测试环境：同步源码、起测试库、编译重启后端、构建前端、起前端预览。`fresh` 清空测试库 |
| `bash tools/dev/fe.sh` | 只更新测试环境的前端（类型检查 + 构建），后端没动时用 |
| `bash tools/dev/regress.sh [名字 ...]` | 全套回归：Go 测试 + 所有检查脚本，每个脚本前清空测试库。可以只跑几个：`regress.sh go live switcher`。结果在 `<工作目录>/logs/summary.txt` |
| `bash tools/dev/dep.sh [backend]` | 部署到 8080：前端构建后覆盖 `web/live`（用户按 Ctrl+F5）；`backend` = 让开发模式的后端重新编译 |
| `python tools/dev/i18n_add.py <词条文件>` | 加或改界面词条（`zh.ts`、`en.ts` 一起改）。词条文件里写 `ZH`、`EN` 两个字典；要覆盖已有的键，把键写进 `ALLOW`；要删的写进 `DROP` |

单独跑一个检查脚本：

```bash
cd "$LOCALAPPDATA/qs-dev/tools" && PW_CHANNEL=msedge node e2e-live.mjs ../shots http://127.0.0.1:4173
```

注意：

- 检查脚本会注册账号、下单，**只能对着测试环境**（4173）。只读的 `e2e-scan.mjs` 例外，可以对着 8080。
- Go 的数据库测试用 `QS_TEST_DB=postgres://qs:test@127.0.0.1:55433/qs_gotest?sslmode=disable`，会删表，绝不能指向 `qs-db`。
- `regress.sh` 跑着的时候不要用 `env_up.sh`、`fe.sh`（会换掉正在测的前端和后端）；`dep.sh` 可以（构建到另一个目录）。
- 在 Claude 桌面版里跑时，`%LOCALAPPDATA%` 会被 Windows 重定向到 `%LOCALAPPDATA%\Packages\Claude_*\LocalCache\Local`，所以工作目录实际在那里；所有 Claude 会话看到的是同一个。
- 测试库放在内存里，Docker Desktop 重启（比如电脑睡眠）后没了，再跑一次 `env_up.sh`。
- 示例策略 `demo-random` 在测试环境是开的（开源版默认关）。它的信号是随机的，当天亏超 5% 会熔断；`regress.sh` 每个脚本前清库，所以不受影响。
