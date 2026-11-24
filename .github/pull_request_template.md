## 改了什么 / What changed

<!-- 一两句话：改了什么、为什么 / One or two sentences: what and why -->

## 怎么验证的 / How it was checked

- [ ] `go test`（用数据库的测试要设 `QS_TEST_DB`，指向临时库 / point `QS_TEST_DB` at a throwaway database）
- [ ] `npx tsc --noEmit -p .` 和 `npx vite build`
- [ ] 界面改动：附截图（宽屏和手机宽度）/ UI changes: screenshots at desktop and phone width

## 确认 / Confirm

- [ ] 没有提交任何密钥、密码、`.env` / No keys, passwords or `.env` committed
- [ ] 不涉及收费、分成、返佣、带单、提现划转（见 CONTRIBUTING.md）/ Nothing involving payments, referrals, signal selling, withdrawals or transfers
