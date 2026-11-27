# 参与开发

[English](CONTRIBUTING.md) | 简体中文

欢迎提问题、提改进、修 bug。下面是怎么把开发环境跑起来、怎么自己验证改动。

## 先说边界

这个项目有几条不会改的原则，提功能前请先看一眼，免得白费工夫：

- **不碰钱**：不做收费、分成、返佣、邀请奖励、带单、资金归集；代码里不加提现和划转。
- **不提供策略**：不内置任何策略、信号、买卖建议、评分。示例代码只演示怎么接。
- **不抓网页**：数据只用官方公开接口，或者部署的人自己填密钥的接口。
- **依赖的许可证**只收 MIT、Apache-2.0、ISC、BSD、0BSD、OFL（字体）。不收 GPL、AGPL。`python tools/dev/licenses.py` 会检查。
- **界面**：纯黑底、白色是唯一的强调色，红绿只表示涨跌和盈亏；字写"会发生什么"，不写代号；所有页面整页不滚动。

## 目录

| 目录 | 内容 |
| --- | --- |
| `backend/` | Go 后端。`cmd/qs` 是入口，`internal/` 下每个包一件事（`trading` 模拟盘引擎、`live` 接交易所、`follow` 策略跟随、`backtest` 回测、`notify` 通知……） |
| `web/` | React + TypeScript + Vite 前端。`src/pages` 页面，`src/charts` 自己画的图，`src/i18n` 中英文 |
| `docs/` | 给使用者的文档；`docs/protocol` 是 qsig/1 协议的机器可读定义 |
| `tools/` | 检查脚本（`e2e-*.mjs`，用 Playwright 真的点一遍界面）和开发用的脚本（`tools/dev`） |
| `compose.yaml` | 给使用的人：一条命令启动 |
| `deploy/docker-compose.yml` | 开发模式：后端直接从源码编译运行，改了代码写一下 `backend/.reload` 就自动重新编译 |

## 跑起来

只是用：`docker compose up -d`（见 README）。

开发：

```bash
docker compose -f deploy/docker-compose.yml up -d    # 数据库 + 后端（开发模式）+ nginx，http://localhost:8080
cd web && npm ci && npm run dev                       # 前端开发服务器（热更新），默认转发到本机后端
```

后端改完：`touch backend/.reload`，容器里会重新编译并重启（看 `docker compose -f deploy/docker-compose.yml logs -f backend`）。

## 验证你的改动

```bash
# 后端：格式、静态检查、测试
cd backend
gofmt -l ./cmd ./internal        # 不应该有输出
go vet ./...
go test ./...                    # 不带数据库的测试

# 带数据库的测试：起一个临时的 PostgreSQL，测试会清空它用到的表，不要指向有数据的库
docker run -d --rm --name qs-test-pg -e POSTGRES_USER=qs -e POSTGRES_PASSWORD=test -e POSTGRES_DB=qs_gotest -p 127.0.0.1:55433:5432 timescale/timescaledb:latest-pg16
QS_TEST_DB='postgres://qs:test@127.0.0.1:55433/qs_gotest?sslmode=disable' go test -p 1 ./...

# 前端：类型检查和构建
cd web
npx tsc --noEmit -p .
npm run build

# 第三方许可证
python tools/dev/licenses.py
```

推送到 `main` 和每个合并请求，GitHub Actions 会自动跑上面的后端测试（带竞态检查，连一个临时数据库）和前端的类型检查、构建（`.github/workflows/ci.yml`）。推 `v` 开头的标签（比如 `v0.1.0`）会构建后端和网页两个 Docker 镜像，发布到这个仓库所属账号的 GitHub 容器仓库（`release.yml`），改动记在 [CHANGELOG.md](CHANGELOG.md)。

界面的检查脚本在 `tools/e2e-*.mjs`：每个脚本开头写了它查什么、怎么跑。它们会注册账号、下模拟单，**只对着测试环境跑，不要对着有真实数据的部署跑**。`tools/dev/` 里是作者本机（Windows + Git Bash）起测试环境、跑全套检查用的脚本，可以参考着改成你自己的。

## 提交之前

- 改了行为就加测试；改了界面就在几种窗口宽度和 125%、150% 缩放下看一眼（很多人的笔记本是 125%）。
- 界面上新加的字，中文（`web/src/i18n/zh.ts`）和英文（`en.ts`）都要有。
- 和钱有关的计算用 Decimal，规则以交易所为准；和币安不一样的地方要在界面或文档里写明。
- 不要提交任何密钥、`.env`、真实的账户信息。
- 数据库改动只加不删：可以建新表、加新字段（新字段要么可以为空、要么有默认值）；不删表、删字段，不改名、不改类型。这样升级以后退回上一个版本，旧版还能接着用升级过的库。要去掉的东西先在一个版本里停用，至少隔一个版本再删。`backend/internal/db/migrate_test.go` 会检查。
- 用户看得到的改动记在 [CHANGELOG.md](CHANGELOG.md) 的"未发布"一节。版本号在 `backend/internal/buildinfo/VERSION`，两次发布之间是 `0.1.0+dev` 这样。

## 报告安全问题

请私下联系维护者，不要公开提 issue。
