# 重启测试环境的后端（:18000）。文件要带 UTF-8 BOM：PowerShell 5.1 没有 BOM 时按系统编码读中文注释，会把下一行吞掉。
# 只连测试库 qs-test-pg，不碰 8080。
# 测试用的主密钥和 qsig 密钥第一次运行时随机生成，存在工作目录里（不进仓库，不打印）。
param([Parameter(Mandatory = $true)][string]$W)

$p = Get-Process qs-run -ErrorAction SilentlyContinue
if ($p) { $p | Stop-Process -Force; Start-Sleep -Milliseconds 800 }

function Secret([string]$file, [scriptblock]$make) {
  $f = Join-Path $W $file
  if (-not (Test-Path $f)) { Set-Content -Path $f -Value (& $make) -NoNewline -Encoding ascii }
  (Get-Content $f -Raw).Trim()
}
# 要兼容 Windows 自带的 PowerShell 5.1（.NET Framework）：没有 RandomNumberGenerator.Fill、Convert.ToHexString
function RandBytes([int]$n) { $b = New-Object byte[] $n; [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b); $b }
$master = Secret 'test-master.key' { [Convert]::ToBase64String((RandBytes 32)) }
$qsig = Secret 'qsig-e2e.secret' { 'e2e-key:' + (-join ((RandBytes 16) | ForEach-Object { $_.ToString('x2') })) + ':e2e-strategy' }

$env:DATABASE_URL = 'postgres://qs:test@127.0.0.1:55433/qs_e2e?sslmode=disable'
$env:QSIG_KEYS = $qsig
$env:QS_MASTER_KEY = $master
# 本机假交易所（D93）：实盘的检查脚本对着它跑，不连币安
$env:QS_LIVE_FAKE = 'e2efakekey-0123456789abcd:e2efakesecret-0123456789abcd'
# Telegram、AI 都指到本机一个不存在的端口：检查脚本自己起假服务
$env:TELEGRAM_BOT_TOKEN = 'test-token-e2e'; $env:TELEGRAM_BASE = 'http://127.0.0.1:18999'
$env:DEEPSEEK_API_KEY = 'test-deepseek'; $env:DEEPSEEK_BASE = 'http://127.0.0.1:18999'
$env:MARKET_SOURCE = 'sim'; $env:HYPERLIQUID = '0'; $env:ONCHAIN = 'sim'
$env:HTTP_ADDR = ':18000'; $env:AUTH_DEV = '1'; $env:DEMO_STRATEGY = '1'; $env:QS_SIGNUP = 'open'
# 对外 Webhook 的检查脚本在本机起一个接收端：测试环境允许发到本机
$env:QS_WEBHOOK_PRIVATE = '1'
# 发信（D100）：发到本机 2525 端口。e2e-mail.mjs 跑的时候在那里起一个收信的，别的时候没人收、发不出去只记日志。
# 开发模式照样开着（AUTH_DEV=1），别的检查脚本还是从页面上拿验证码
$env:SMTP_HOST = '127.0.0.1'; $env:SMTP_PORT = '2525'; $env:SMTP_USER = ''; $env:SMTP_PASSWORD = ''; $env:MAIL_FROM = 'qs-test@example.com'
# 新闻（18 第 8.6 条）：去本机 2526 端口取。e2e-news.mjs 跑的时候在那里起假的 RSS 和 X 接口，别的时候取不到、只记日志
$env:NEWS_FEEDS = 'http://127.0.0.1:2526/rss.xml,http://127.0.0.1:2526/atom.xml'
$env:X_BEARER_TOKEN = 'test-x-e2e'; $env:X_API_BASE = 'http://127.0.0.1:2526'; $env:X_ACCOUNTS = 'exchange_notice'
# 交易所公告（18 第 10.2 条）：Bybit、OKX 的公告接口也指到同一个本机假服务
$env:NEWS_NOTICE_BASE = 'http://127.0.0.1:2526'

Start-Process -FilePath (Join-Path $W 'qs-run.exe') -WorkingDirectory $W -WindowStyle Hidden `
  -RedirectStandardOutput (Join-Path $W 'backend.log') -RedirectStandardError (Join-Path $W 'backend.err')
