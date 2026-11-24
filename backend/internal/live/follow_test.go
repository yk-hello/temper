package live

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/auth"
	"qs/internal/follow"
	"qs/internal/portfolio"
	"qs/internal/trading"
)

// 18 第 5.2 条：策略跟随驱动交易所账户。走的是和模拟盘同一套跟随规则，单子发到（假）交易所。

type noPaper struct{}

func (noPaper) OwnsPaper(context.Context, int64, int64) bool { return false }
func (noPaper) PaperAccounts(context.Context, int64) ([]auth.PaperAccount, error) {
	return nil, nil
}
func (noPaper) NewPaper(context.Context, *auth.User, string) (*auth.PaperAccount, error) {
	return nil, auth.ErrTooManyAccounts
}

func TestFollowOnLive(t *testing.T) {
	e := newEnv(t)
	a := e.user("dave@example.com")
	_, me := a.do("GET", "/api/me", nil)
	uid := int64(me["user"].(map[string]any)["id"].(float64))
	st, r := a.do("POST", "/api/live/keys", map[string]any{"env": "fake", "label": "Main", "key": fKey, "secret": fSecret, "trade_password": "trade-pw-8899"})
	if st != 200 {
		t.Fatalf("密钥：%d %v", st, r)
	}
	st, r = a.do("POST", "/api/live/accounts", map[string]any{"key_id": r["key"].(map[string]any)["id"], "name": "Main"})
	if st != 200 {
		t.Fatalf("账户：%d %v", st, r)
	}
	acct := int64(r["id"].(float64))
	base := "/api/trade/" + strconv.FormatInt(acct, 10)
	a.until(base, func(v map[string]any) bool { return v["wallet"] == "10000" })

	// 模型组合在模拟盘上（它自己的账户），跟随者在交易所账户上
	paper := trading.New(e.books, venue, trading.NewSpecs(trading.LooseSpecs()), e.desks.Log)
	pf := portfolio.New("s1", -1, venue, venue, e.books, paper)
	clock := time.Now()
	pf.Now = func() time.Time { return clock }
	m := follow.New(trading.Mux{Paper: paper, Live: e.desks}, noPaper{}, func(id string) *portfolio.Portfolio {
		if id == "s1" {
			return pf
		}
		return nil
	}, e.desks.Log)
	m.Now = func() time.Time { return clock }
	m.Persist = func([]follow.Event) error { return nil }
	armed := false
	m.LiveAcct = func(_ context.Context, user, id int64) (bool, bool) { return user == uid && id == acct, armed }
	m.TradePassword = func(u *auth.User, pw string) error {
		if pw != "trade-pw-8899" {
			return auth.ErrBadTradePw
		}
		return nil
	}
	pf.OnChange = func(c []portfolio.Change) { m.Process("s1", c) }
	u := &auth.User{ID: uid, Lang: "zh"}
	req := follow.StartReq{Strategy: "s1", Account: acct, Alloc: decimal.NewFromInt(5000), Confirm: true, TradePassword: "trade-pw-8899"}

	// 没确认"这是真钱"、别人的账户、二级密码错：都不开
	if _, err := m.Start(context.Background(), u, req); err == nil || err.Error() != "LIVE_NOT_ARMED" {
		t.Fatalf("没确认：%v", err)
	}
	armed = true
	// D99：交易所账户只能跟自己的策略。没接上这项检查时一律不开
	if _, err := m.Start(context.Background(), u, req); err == nil || err.Error() != "LIVE_OWN_STRATEGY" {
		t.Fatalf("没接上策略归属检查：%v", err)
	}
	strategyOwner := uid
	m.LiveStrategy = func(_ context.Context, who *auth.User, id int64, s string) string {
		if s == "s1" && who.ID == strategyOwner && id == acct {
			return ""
		}
		return "LIVE_OWN_STRATEGY"
	}
	strategyOwner = uid + 7 // 别人的策略
	if _, err := m.Start(context.Background(), u, req); err == nil || err.Error() != "LIVE_OWN_STRATEGY" {
		t.Fatalf("别人的策略接到自己的交易所账户：%v", err)
	}
	if got := m.LiveCheck(context.Background(), u, "s1", []int64{acct, acct + 1, 5}); got[acct] != "LIVE_OWN_STRATEGY" || got[acct+1] != "NOT_FOUND" || len(got) != 2 {
		t.Fatalf("给向导的检查（别人的策略）：%v", got)
	}
	strategyOwner = uid
	if got := m.LiveCheck(context.Background(), u, "s1", []int64{acct}); got[acct] != "" {
		t.Fatalf("给向导的检查（自己的策略）：%v", got)
	}

	if _, err := m.Start(context.Background(), &auth.User{ID: uid + 1}, req); err == nil || err.Error() != "NOT_FOUND" {
		t.Fatalf("别人的账户：%v", err)
	}
	bad := req
	bad.TradePassword = "nope"
	if _, err := m.Start(context.Background(), u, bad); err == nil || err.Error() != "BAD_TRADE_PASSWORD" {
		t.Fatalf("二级密码错：%v", err)
	}
	a.do("POST", "/api/live/accounts/"+strconv.FormatInt(acct, 10)+"/arm", map[string]any{"trade_password": "trade-pw-8899"})
	v, err := m.Start(context.Background(), u, req)
	if err != nil {
		t.Fatalf("开启：%v", err)
	}

	apply := func(id, w, urg string) {
		clock = clock.Add(time.Second)
		pf.Apply(id, clock.UnixMilli(), clock.Add(time.Minute).UnixMilli(), false,
			[]portfolio.Target{{Instrument: "BTC-PERP", Weight: decimal.RequireFromString(w), RefPrice: decimal.NewFromInt(60000), Urgency: urg, MaxSlipBps: 20}})
	}
	settle := func() { // 等这个账户的队列做完
		for i := 0; i < 200 && !m.Idle(); i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
	last := func() follow.ExecView {
		g, err := m.Get(u, v.ID)
		if err != nil || len(g.Records) == 0 {
			t.Fatalf("执行记录：%v", err)
		}
		return g.Records[0]
	}
	posQty := func(want string) {
		a.until(base, func(v map[string]any) bool {
			ps := list(v, "positions")
			if want == "0" {
				return len(ps) == 0
			}
			return len(ps) == 1 && ps[0].(map[string]any)["qty"] == want
		})
	}

	// 立刻执行：0.5 × 5000 ÷ 60000 = 0.0416 → 0.041，交易所上真的有了这个仓位，逐仓 3 倍。
	// 下单回复里不带均价（币安模拟交易实测就这样，11-16）：记录里的成交价、手续费照样是真的（按成交推送）
	e.fake.NoAvgInReply = true
	apply("a", "0.5", "immediate")
	settle() // 交易所账户的单在它自己的队列里做（18 第 9.1 条）
	if x := last(); x.State != "filled" || x.Filled.String() != "0.041" || x.Fee.Sign() <= 0 || x.AvgPx.Sign() <= 0 {
		t.Fatalf("立刻执行：%+v", x)
	}
	if x := last(); x.AvgPx.LessThan(decimal.NewFromInt(59000)) || x.AvgPx.GreaterThan(decimal.NewFromInt(61000)) {
		t.Fatalf("成交价按成交推送记：%s", x.AvgPx)
	}
	e.fake.NoAvgInReply = false
	posQty("0.041")
	vw := a.until(base, func(v map[string]any) bool {
		s, _ := v["settings"].(map[string]any)["BTC-PERP"].(map[string]any)
		return s != nil && s["mode"] == "isolated" && s["leverage"] == float64(3)
	})
	_ = vw
	_, fills := a.do("GET", base+"/history?kind=fills", nil)
	if src := list(fills, "items")[0].(map[string]any)["source"]; src != "follow" {
		t.Fatalf("交易所上的成交来源：%v", src)
	}

	// 挂单执行：在买一挂只挂单，价格跌下来成交；跟随从本地记录看到成交
	apply("b", "0.8", "passive")
	settle()
	if x := last(); x.State != "pending" {
		t.Fatalf("挂着：%+v", x)
	}
	e.price("BTCUSDT", 59990)
	for i := 0; i < 50 && last().State == "pending"; i++ {
		settle()
		time.Sleep(100 * time.Millisecond)
		m.Tick()
	}
	if x := last(); x.State != "filled" || x.Filled.String() != "0.025" {
		t.Fatalf("挂单成交：%+v", x)
	}
	posQty("0.066")

	// 权重 0：全平
	e.price("BTCUSDT", 60000)
	apply("c", "0", "immediate")
	settle()
	posQty("0")
	if x := last(); x.State != "filled" {
		t.Fatalf("全平：%+v", x)
	}

	// 暂停后，策略归属变了（比如是以前开的、跟的是别人的策略）：不能再恢复
	if _, err := m.Pause(u, v.ID); err != nil {
		t.Fatalf("暂停：%v", err)
	}
	strategyOwner = uid + 7
	if _, err := m.Resume(u, v.ID); err == nil || err.Error() != "LIVE_OWN_STRATEGY" {
		t.Fatalf("跟别人策略的交易所账户跟随恢复了：%v", err)
	}
	strategyOwner = uid
	if _, err := m.Resume(u, v.ID); err != nil {
		t.Fatalf("自己的策略能恢复：%v", err)
	}

	// 停止：撤掉跟随的挂单（这里没有），状态变成已停止
	if _, err := m.Stop(u, v.ID, true); err != nil {
		t.Fatalf("停止：%v", err)
	}
}

// 18 第 9.1 条：交易所卡住（这里让假交易所每个请求慢 2 秒）时，模拟账户的跟随照样当场成交，查跟随状态也不等。
type onePaper struct{ acct int64 }

func (a onePaper) OwnsPaper(_ context.Context, _, acct int64) bool { return acct == a.acct }
func (a onePaper) PaperAccounts(context.Context, int64) ([]auth.PaperAccount, error) {
	return []auth.PaperAccount{{ID: a.acct, Name: "Paper 1"}}, nil
}
func (onePaper) NewPaper(context.Context, *auth.User, string) (*auth.PaperAccount, error) {
	return nil, auth.ErrTooManyAccounts
}

func TestSlowExchangeDoesNotBlock(t *testing.T) {
	e := newEnv(t)
	a := e.user("ivy@example.com")
	_, me := a.do("GET", "/api/me", nil)
	uid := int64(me["user"].(map[string]any)["id"].(float64))
	_, r := a.do("POST", "/api/live/keys", map[string]any{"env": "fake", "label": "Main", "key": fKey, "secret": fSecret, "trade_password": "trade-pw-8899"})
	_, r = a.do("POST", "/api/live/accounts", map[string]any{"key_id": r["key"].(map[string]any)["id"], "name": "Main"})
	acct := int64(r["id"].(float64))
	a.do("POST", "/api/live/accounts/"+strconv.FormatInt(acct, 10)+"/arm", map[string]any{"trade_password": "trade-pw-8899"})
	base := "/api/trade/" + strconv.FormatInt(acct, 10)
	a.until(base, func(v map[string]any) bool { return v["wallet"] == "10000" })

	paper := trading.New(e.books, venue, trading.NewSpecs(trading.LooseSpecs()), e.desks.Log)
	const pAcct = 7
	paper.Open(pAcct, decimal.NewFromInt(10000))
	pf := portfolio.New("s1", -1, venue, venue, e.books, paper)
	m := follow.New(trading.Mux{Paper: paper, Live: e.desks}, onePaper{pAcct}, func(id string) *portfolio.Portfolio {
		if id == "s1" {
			return pf
		}
		return nil
	}, e.desks.Log)
	m.Persist = func([]follow.Event) error { return nil }
	m.LiveAcct = func(_ context.Context, user, id int64) (bool, bool) { return user == uid && id == acct, true }
	m.TradePassword = func(*auth.User, string) error { return nil }
	m.LiveStrategy = func(context.Context, *auth.User, int64, string) string { return "" }
	pf.OnChange = func(c []portfolio.Change) { m.Process("s1", c) }
	u := &auth.User{ID: uid, Lang: "zh"}
	ctx := context.Background()
	vl, err := m.Start(ctx, u, follow.StartReq{Strategy: "s1", Account: acct, Alloc: decimal.NewFromInt(5000), Confirm: true, TradePassword: "x"})
	if err != nil {
		t.Fatalf("交易所账户开启：%v", err)
	}
	vp, err := m.Start(ctx, u, follow.StartReq{Strategy: "s1", Account: pAcct, Alloc: decimal.NewFromInt(5000), Confirm: true})
	if err != nil {
		t.Fatalf("模拟账户开启：%v", err)
	}

	e.fake.SetDelay(2 * time.Second)
	t0 := time.Now()
	pf.Apply("a", time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli(), false,
		[]portfolio.Target{{Instrument: "BTC-PERP", Weight: decimal.RequireFromString("0.5"), RefPrice: decimal.NewFromInt(60000), Urgency: "immediate", MaxSlipBps: 20}})
	if d := time.Since(t0); d > 500*time.Millisecond {
		t.Fatalf("交易所慢，处理信号却等了 %v", d)
	}
	// 模拟账户当场成交
	if b, _ := paper.Brief(pAcct); !b.Pos["BTC-PERP"].Qty.Equal(decimal.RequireFromString("0.041")) {
		t.Fatalf("模拟账户应该当场成交：%v", b.Pos["BTC-PERP"].Qty)
	}
	// 交易所的单还在路上：查跟随状态、每秒的检查都不用等
	t1 := time.Now()
	list := m.List(u)
	m.Tick()
	if d := time.Since(t1); d > 300*time.Millisecond || len(list) != 2 {
		t.Fatalf("查跟随状态等了 %v（%d 个）", d, len(list))
	}
	g, _ := m.Get(u, vl.ID)
	if len(g.Records) == 0 || g.Records[0].State != "pending" {
		t.Fatalf("交易所账户这时应该还在执行：%+v", g.Records)
	}
	// 交易所回话以后成交
	for i := 0; i < 1000 && !m.Idle(); i++ { // 每个请求慢 2 秒：设保证金模式、杠杆、读费率、下单，一共八九秒
		time.Sleep(20 * time.Millisecond)
	}
	if !m.Idle() {
		t.Fatal("交易所账户的队列一直没做完")
	}
	g, _ = m.Get(u, vl.ID)
	if g.Records[0].State != "filled" || g.Records[0].Filled.String() != "0.041" {
		t.Fatalf("交易所账户最后应该成交：%+v", g.Records[0])
	}
	e.fake.SetDelay(0)
	gp, _ := m.Get(u, vp.ID)
	if gp.Records[0].State != "filled" {
		t.Fatalf("模拟账户的记录：%+v", gp.Records[0])
	}
}
