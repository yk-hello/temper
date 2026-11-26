# 让策略一直跑

> 平台不运行你的代码。策略是你自己的程序，这一页说怎么让它一直开着、崩了自己起来、重启以后接得上。

要用的文件都在 [examples/run/](examples/run/)：`strategy.env.example`（设置）、`docker-compose.yml`（Docker）、`my-strategy.service`（Linux）、`run_forever.bat`（Windows）。

## 一、要一直开着的是两样东西

| | 它停了会怎样 |
| --- | --- |
| **平台**（Temper 本身） | 收不到信号。模拟盘不撮合，条件单不触发。实盘账户里的止盈止损是挂在币安的条件单，币安照常执行 |
| **你的策略程序** | 平台保持现在的仓位，不加也不减，止损照常。超过 180 秒没收到心跳，策略页显示"策略离线"。**平台现在不会推送离线提醒**，要自己看 |

最省事的做法是两样放在同一台机器上，策略里的平台地址填 `http://localhost:8080`。

## 二、放在哪台机器上

| | 好处 | 坏处 | 适合 |
| --- | --- | --- | --- |
| 自己的电脑 | 不花钱 | 睡眠、关机、Windows 更新重启、家里断网，都会停 | 先试跑、模拟盘 |
| 云服务器 | 一直开着；可以选离币安近的地区 | 每月要钱；要自己管安全（见 [README](../README.zh-CN.md) 的"安全"） | 长期跑、实盘 |

想知道服务器放在别的地区下单会慢多少，在模拟盘里"账户 → 交易偏好 → 模拟下单延迟"选那个地区试。

### 用自己的 Windows 电脑

1. **不让它睡**："设置 → 系统 → 电源"（笔记本上叫"电源和电池"），插着电源时的睡眠选"从不"；或者在 PowerShell 里运行 `powercfg /change standby-timeout-ac 0`。笔记本合上盖子默认也会睡："控制面板 → 电源选项 → 选择关闭笔记本计算机盖的功能"，接通电源时选"不采取任何操作"。
2. **Docker Desktop 开机自己起来**：Docker Desktop 的设置里 General 页，勾上"Start Docker Desktop when you sign in to your computer"。平台的几个容器都设了自动重启，Docker 一起来它们就跟着起来。
3. **Windows 更新别在半夜重启**："设置 → Windows 更新 → 高级选项 → 活动时间"。
4. 电脑重启以后，要**登录一次 Windows**，Docker Desktop 和策略才会起来。

## 三、崩了自己起来

三种办法选一种。三种读的都是同一个设置文件：把 `strategy.env.example` 复制成 `strategy.env`，填上平台地址、key id、secret、策略名。

```
QS_BASE=http://localhost:8080
QS_KEY=你的 key id
QS_SECRET=你的 secret
QS_STRATEGY=my-strategy
```

一行一个，等号两边不要空格，值不要加引号。`strategy.env` 里有密钥：不要提交到 git（把它写进 `.gitignore`），不要发给别人。

### A. Docker（Windows、Mac、Linux 都一样）

把 `docker-compose.yml`、`strategy.env`、你的策略（连同 `qsclient.py`、`marketdata.py`）放在同一个目录：

```bash
docker compose up -d        # 启动
docker compose logs -f      # 看输出
docker compose down         # 停
```

- `restart: unless-stopped`：程序崩了，Docker 马上重启它；电脑重启、Docker 起来以后也会自己起来。只有你 `docker compose down` 或 `stop` 才会停。
- 平台地址：Docker Desktop（Windows、Mac）上填 `QS_BASE=http://host.docker.internal:8080`。Linux 上平台默认只听本机，容器连不到，把文件里 `extra_hosts` 两行换成 `network_mode: host`，再填 `QS_BASE=http://localhost:8080`。
- 日志最多留 30 MB（`max-size` 10 MB × 3 份），不会写满硬盘。
- 第一次启动要下载 Python 镜像（几十 MB）。

### B. Linux 服务器：systemd

`my-strategy.service` 开头的注释就是全部步骤：建一个不能登录的用户、把文件放到 `/opt/my-strategy`、`chmod 600 strategy.env`、`systemctl enable --now`。

- `Restart=always` 加 `RestartSec=10`：程序退出 10 秒后重启；`enable` 了开机就会起来。
- 看输出：`journalctl -u my-strategy -f`。systemd 的日志会自己轮换，不用管。

### C. Windows 不用 Docker：循环脚本

把 `run_forever.bat`、`strategy.env`、你的策略放在同一个文件夹，双击 `run_forever.bat`。

- 策略退出或者崩了，10 秒后重新起来。关掉这个黑窗口才会停。
- 输出写在旁边的 `strategy.log`，时间长了记得清一下。
- 默认跑 `template_rule.py`，换成你的文件：`run_forever.bat my_strategy.py`，或者改脚本里 `SCRIPT=` 那一行。
- 登录 Windows 时自己起来：按 Win+R，输入 `shell:startup` 回车，把 `run_forever.bat` 的快捷方式放进打开的文件夹。
- 命令行里输入 `python --version` 要能看到版本号。看不到的话，装 Python 时勾上"Add python.exe to PATH"。

## 四、重启以后接得上吗

- **目标仓位重复发不会多下单。** qsig/1 发的是"应该持有多少"，不是"买多少"。重启后第一次算出来的和重启前一样，平台回"维持现状"，不下单。所以程序随时停、随时重启都安全。
- **算指标要的历史，启动时重新取。** 均线这类要过去几百根 K 线的，启动时从币安重新取一遍就行（[examples/marketdata.py](examples/marketdata.py)），不用自己存。
- **自己的状态要存文件。** 比如"上次开仓是什么时候"。写的时候先写到临时文件，再改名替换（Python 里写完临时文件后用 `os.replace`），这样写到一半断电，旧文件也还在。
- **启动时先看一眼平台那边的状态**：`c.portfolio()` 里有现在的目标和仓位、是否熔断，以及被平台平掉的仓位和原因（`closed_by`）。
- **网络断了**：这一轮不发，下一轮重新取数据、重新算、重新发。两个模板都是这么写的。
- **自己发过 halt 的**：重启以后不会自己恢复，要部署的人在管理后台手动恢复。这样设计，是为了防止程序一重启就把暂停解开。

## 五、心跳别被卡住

- 心跳 30–60 秒发一次，超过 180 秒没收到就算离线。发出的信号也算一次心跳。
- **判断要花很久时，心跳单独放一个线程。** 问一次大模型可能要几十秒到几分钟。心跳和判断放在一个循环里的话，判断卡住，心跳也跟着停。[examples/template_ai.py](examples/template_ai.py) 里的 `keep_alive()` 就是单独的心跳线程。给模型请求设超时，比如 120 秒。
- **程序还活着，但数据或模型出了问题**：发 `c.heartbeat("degraded", note="币安 K 线取不到")`，平台会提示跟着这个策略的人。

## 六、时钟要准

签名里带着时间，和平台差超过 30 秒就拒绝（错误码 `TIMESTAMP_SKEW`）。

- Windows："设置 → 时间和语言 → 日期和时间"，打开自动设置时间，点"立即同步"。
- Linux：`timedatectl` 里要看到 `System clock synchronized: yes`。
- Docker Desktop 里的程序用的是 Docker 虚拟机的时钟，平时跟着电脑走。电脑刚从睡眠醒来就报 `TIMESTAMP_SKEW` 的话，重启一下 Docker Desktop。

## 七、日志

- 每次判断记一行：时间、用了哪些数据、算出的目标、平台的回复。出了问题才查得清。大模型策略还要记提示词和模型原始输出，见 [strategy-guide.md](strategy-guide.md) 第七节。
- 日志里不要打印 secret。
- 别让日志写满硬盘：Docker 和 systemd 已经限制了大小，Windows 循环脚本的 `strategy.log` 要自己定期清。

## 八、上线前自己试一遍

- [ ] 拔掉网线一分钟再插上：程序自己恢复，策略页从"离线"变回在线
- [ ] 结束策略进程（任务管理器结束 python，或者 `docker kill`）：10 秒内自己起来
- [ ] 重启电脑并登录：平台和策略都自己起来了
- [ ] 重启以后没有多下单：信号记录里，重启后第一条是"维持现状"
- [ ] 时钟开着自动同步
- [ ] `strategy.env` 没有进 git
