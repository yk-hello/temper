# Telegram 通知（可选）

不配也能用：所有通知都在网页右上角的铃铛里。配了以后，每个人可以把自己的账号绑到自己的 Telegram，手机上收提醒。

一台服务器用一个 bot，由部署的人自己建。这个项目不提供公共 bot。

## 部署的人：建 bot（一次）

1. 在 Telegram 里搜 `@BotFather`（认准蓝色认证标），发 `/newbot`。
2. 按提示起名字和用户名（用户名要以 `bot` 结尾）。BotFather 回一串 token，形如 `123456789:AA...`。
3. 把 token 写进 `.env`（没有就新建一个，和 `compose.yaml` 放一起）：

   ```
   TELEGRAM_BOT_TOKEN=123456789:AA...
   ```

4. 让它生效：再运行一次 `docker compose up -d`。

token 相当于这个 bot 的密码：只放在 `.env`，不要贴到聊天、截图或代码仓库里。泄露了在 BotFather 里发 `/revoke` 换一个。

后端用长轮询收消息，**不需要公网地址和 HTTPS**，放在家里的电脑上也能用。服务器要能访问 `api.telegram.org`（国内网络通常要代理，按 Docker 的方式给后端容器配 `HTTPS_PROXY`）。

## 每个用户：绑定自己的账号

1. 网页右上角进账户页，"通知"一节里 Telegram 那一行点"绑定"。
2. 会打开这个 bot 的对话，点 **Start**。网页上的状态自动变成"已绑定"。
3. 绑定码 10 分钟有效、只能用一次。

解除：网页上点"解除绑定"，或者在 Telegram 里给 bot 发 `/stop`（同一个 Telegram 绑过几个账号的，都会解开）。

## 发什么

在账户页的"通知"一节按事件逐项开关。默认开的：快到强平价和已被强平、保证金率过高、纪律锁触发、机器人被暂停、自己设的到价提醒。高光默认只在网页里，想在 Telegram 收要自己打开。

发送失败（网络断了）会过一会儿重试。

## 不用 Telegram：对外 Webhook

"通知"里也可以填一个 Webhook 地址，勾选的事件 POST 一份 JSON 过去（`event`、`time`、`params`、写好的一句话 `text`），可以接自己的机器人、企业微信、飞书、n8n。每条都带签名：`X-QS-Signature` = HMAC-SHA256(密钥, `X-QS-Timestamp` + "." + 请求体)，密钥在保存地址时显示一次。默认不发到本机和内网地址（`QS_WEBHOOK_PRIVATE=1` 可以打开）。
