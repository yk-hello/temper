package main

// 本机命令（2026-11-15，用户："加一个这个工具吧"）：自己部署的人在部署的那台电脑上运行，
// 忘了二级密码、丢了二次验证的手机时自救。开源以后每个人跑自己的一份，没有"找作者重置"这回事；
// 没配发信邮箱、或者邮箱也进不去的时候，网页上的"忘了二级密码？"走不通，就用这个。
//
// 只有能进这台电脑的人（能运行 docker compose exec）才能用，不经过网页，所以不会降低安全性。
//
//	docker compose exec backend qs reset-password 你的账户名
//	docker compose exec backend qs reset-trade-password 你的邮箱
//	docker compose exec backend qs reset-2fa 你的邮箱
//
// 邮箱也可以换成用户编号。

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"qs/internal/auth"
	"qs/internal/config"
)

const cliUsage = `本机命令（在部署的那台电脑上运行）：

  qs reset-password <账户名或用户编号>       登录密码换成一个随机的新密码（显示在这里），所有设备退出登录
  qs reset-trade-password <邮箱或用户编号>   清空二级密码。之后在账户页设一个新的，不用输旧的
  qs reset-2fa <邮箱或用户编号>              关掉二次验证（手机丢了、验证器删了时用），登录只要密码

用 Docker 部署时在前面加 docker compose exec backend，例如：
  docker compose exec backend qs reset-trade-password you@example.com
`

// runCLI 有命令行参数时走这里，不启动服务。返回退出码。
func runCLI(args []string) int {
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(cliUsage)
		return 0
	case "reset-password", "reset-trade-password", "reset-2fa":
	default:
		fmt.Fprintf(os.Stderr, "不认识的命令 %q。\n\n%s", args[0], cliUsage)
		return 2
	}
	if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
		fmt.Fprintf(os.Stderr, "要写是哪个账号（邮箱或用户编号）。\n\n%s", cliUsage)
		return 2
	}
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(os.Stderr, "找不到数据库地址（DATABASE_URL）。请在部署的容器里运行：docker compose exec backend qs "+args[0]+" "+args[1])
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "连不上数据库：", err)
		return 1
	}
	defer pool.Close()
	who := strings.TrimSpace(args[1])
	var id int64
	var email string
	err = pool.QueryRow(ctx, `select id, coalesce(name, email, '') from users where name = lower($1) or lower(email) = lower($1) or id::text = $1 order by id limit 1`, who).Scan(&id, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		fmt.Fprintf(os.Stderr, "没有这个账号：%s（邮箱写全，或者写用户编号）\n", who)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "查账号出错：", err)
		return 1
	}
	name := email
	if name == "" {
		name = fmt.Sprintf("用户 %d（只绑了钱包）", id)
	}
	svc := &auth.Service{Pool: pool}
	switch args[0] {
	case "reset-password":
		pw := randomPassword()
		if err := svc.SetPasswordLocal(ctx, id, pw); err != nil {
			fmt.Fprintln(os.Stderr, "重设登录密码出错：", err)
			return 1
		}
		fmt.Printf("已把 %s 的登录密码换成：\n\n  %s\n\n所有设备都退出登录了。用它登录后，到账户页的\"登录密码\"里改成你自己的。\n", name, pw)
	case "reset-trade-password":
		if err := svc.ClearTradePassword(ctx, id); err != nil {
			fmt.Fprintln(os.Stderr, "清空二级密码出错：", err)
			return 1
		}
		// 和网页上重置一样（D107）：交易所账户回到"未开启"，开新仓要用新的二级密码重新开启；减仓、平仓照常
		_, _ = pool.Exec(ctx, `update live_accounts set armed=false where usr=$1 and archived_at is null`, id)
		fmt.Printf("已清空 %s 的二级密码，所有设备都退出登录了，交易所账户暂停开新仓。\n登录后到账户页设一个新的二级密码（不用输旧的），再到\"交易所账户\"里重新开启。\n", name)
	case "reset-2fa":
		if err := svc.Reset2FA(ctx, id); err != nil {
			fmt.Fprintln(os.Stderr, "关二次验证出错：", err)
			return 1
		}
		fmt.Printf("已关掉 %s 的二次验证，恢复码也作废了。现在只用密码就能登录，建议登录后在账户页重新开启。\n", name)
	}
	return 0
}

// randomPassword 16 位，去掉容易看错的 0 O 1 l I。
func randomPassword() string {
	const abc = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = abc[int(b[i])%len(abc)]
	}
	return string(b)
}
