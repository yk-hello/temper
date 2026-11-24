package discipline

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"qs/internal/trading"
)

// 假引擎：权益和平仓记录由测试直接定。
type fakeEngine struct {
	equity   decimal.Decimal
	closes   []trading.ClosedTrade
	seq      int64
	canceled int
	notes    []string
}

func (f *fakeEngine) Brief(int64) (*trading.Brief, error) {
	return &trading.Brief{Equity: f.equity}, nil
}
func (f *fakeEngine) CancelOpening(int64, string) (int, error) { f.canceled++; return 1, nil }
func (f *fakeEngine) ClosedTrades(_, after int64) ([]trading.ClosedTrade, int64) {
	if f.seq <= after {
		return nil, after
	}
	c := f.closes
	f.closes = nil
	return c, f.seq
}
func (f *fakeEngine) Note(_ int64, kind, _, reason string) error {
	f.notes = append(f.notes, kind+":"+reason)
	return nil
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// 台北时间 2026-11-06 12:00（UTC+8）
var noon = time.Date(2026, 11, 6, 4, 0, 0, 0, time.UTC)

type rig struct {
	t   *testing.T
	f   *fakeEngine
	m   *Manager
	now time.Time
}

func newRig(t *testing.T, equity string) *rig {
	f := &fakeEngine{equity: d(equity)}
	r := &rig{t: t, f: f, m: New(f, nil), now: noon}
	r.m.Now = func() time.Time { return r.now }
	r.m.Register(1, 100)
	r.m.SetTZ(1, 480)
	r.m.Tick()
	return r
}

func (r *rig) eq(v string) { r.f.equity = d(v); r.m.Tick() }

func (r *rig) lock() *Lock {
	r.t.Helper()
	for _, a := range r.m.View(1).Accounts {
		if a.Account == 100 {
			return a.Lock
		}
	}
	return nil
}

func (r *rig) wantLock(rule string) {
	r.t.Helper()
	l := r.lock()
	if l == nil || l.Rule != rule {
		r.t.Fatalf("应该因为 %s 锁定，实际 %+v", rule, l)
	}
}

func (r *rig) wantFree() {
	r.t.Helper()
	if l := r.lock(); l != nil {
		r.t.Fatalf("不应该锁定：%+v", l)
	}
}

func open(p decimal.Decimal, qty, stop string) trading.PreOrder {
	return trading.PreOrder{Account: 100, Instrument: "BTC-PERP", Side: "buy", Source: "manual", Qty: d(qty), Price: p, Stop: d(stop), Equity: d("10000")}
}

// 4.13 手算：开盘 10,000，9,501 不锁，9,500 锁；过了当地 0 点解开，按新权益重新算
func TestDailyLossByHand(t *testing.T) {
	r := newRig(t, "10000")
	r.eq("9501")
	r.wantFree()
	r.eq("9500")
	r.wantLock(RuleDaily)
	l := r.lock()
	// 明天台北 0 点 = 今天 UTC 16:00
	if want := time.Date(2026, 11, 6, 16, 0, 0, 0, time.UTC).UnixMilli(); l.Until != want {
		t.Fatalf("解开时间 %d，应 %d", l.Until, want)
	}
	if r.f.canceled != 1 || len(r.f.notes) != 1 || r.f.notes[0] != "discipline_lock:daily_loss" {
		t.Fatalf("锁定时要撤开仓单并提醒：撤 %d 次，提醒 %v", r.f.canceled, r.f.notes)
	}
	// 跨日
	r.now = time.Date(2026, 11, 6, 16, 0, 1, 0, time.UTC)
	r.eq("9500")
	r.wantFree()
	v := r.m.View(1).Accounts[0]
	if !v.DayOpen.Equal(d("9500")) {
		t.Fatalf("今天的开盘权益 %s，应 9500", v.DayOpen)
	}
	// 新的一天按 9500 算：亏 5% 是 475
	r.eq("9025.01")
	r.wantFree()
	r.eq("9025")
	r.wantLock(RuleDaily)
}

// 4.15 手算：开盘 10,000，1R = 100。到过 10,300 再回到 10,100 锁；没到过不锁
func TestGiveBackByHand(t *testing.T) {
	r := newRig(t, "10000")
	r.eq("10299")
	r.eq("10100")
	r.wantFree() // 没到过 +3R
	r = newRig(t, "10000")
	r.eq("10300")
	r.eq("10101")
	r.wantFree()
	r.eq("10100")
	r.wantLock(RuleGiveBack)
}

// 4.16 手算：最高 12,000 跌到 10,200（15%）锁，只能走解锁；解锁后最高点重置
func TestDrawdownByHand(t *testing.T) {
	r := newRig(t, "12000")
	// 换天，不让单日亏损先触发
	r.now = r.now.Add(24 * time.Hour)
	r.eq("11000")
	r.now = r.now.Add(24 * time.Hour)
	r.eq("10500")
	r.now = r.now.Add(24 * time.Hour)
	r.eq("10201")
	r.wantFree()
	r.now = r.now.Add(24 * time.Hour)
	r.eq("10200")
	r.wantLock(RuleDrawdown)
	if r.lock().Until != 0 {
		t.Fatal("最大回撤不会到时间解开")
	}
	// 过一天也不解
	r.now = r.now.Add(48 * time.Hour)
	r.eq("10200")
	r.wantLock(RuleDrawdown)
	// 解锁：10 分钟后生效
	if err := r.m.RequestUnlock(1, 100); err != nil {
		t.Fatal(err)
	}
	r.now = r.now.Add(9 * time.Minute)
	r.eq("10200")
	r.wantLock(RuleDrawdown)
	r.now = r.now.Add(time.Minute)
	r.eq("10200")
	r.wantFree()
	if v := r.m.View(1).Accounts[0]; !v.Peak.Equal(d("10200")) {
		t.Fatalf("解锁后最高点应重置为 10200，实际 %s", v.Peak)
	}
	r.eq("10200")
	r.wantFree() // 不会马上又锁
}

// 4.17 手算：连亏 3 笔冷静 30 分钟，到时间自动解开；赢一笔清零；AI 管的币不算
func TestLossStreakByHand(t *testing.T) {
	r := newRig(t, "10000")
	loss := func(n int) {
		for i := 0; i < n; i++ {
			r.f.seq++
			r.f.closes = []trading.ClosedTrade{{OrderID: "o", Instrument: "BTC-PERP", Net: d("-20")}}
			r.m.Tick()
		}
	}
	loss(2)
	r.f.seq++
	r.f.closes = []trading.ClosedTrade{{OrderID: "o", Instrument: "BTC-PERP", Net: d("50")}}
	r.m.Tick()
	loss(2)
	r.wantFree() // 中间赢过一笔，只连亏了 2 笔
	loss(1)
	r.wantLock(RuleStreak)
	if want := r.now.Add(30 * time.Minute).UnixMilli(); r.lock().Until != want {
		t.Fatalf("冷静到 %d，应 %d", r.lock().Until, want)
	}
	r.now = r.now.Add(30 * time.Minute)
	r.m.Tick()
	r.wantFree()
	if s := r.m.View(1).Accounts[0].Streak; s != 0 {
		t.Fatalf("解开后连亏应清零，实际 %d", s)
	}
	// AI 管的币不算
	r.m.AIManaged = func(int64, string) bool { return true }
	loss(5)
	r.wantFree()
}

// 第一次看这个账户时，以前的亏损单不算连亏：上线第一秒不能把老用户锁住
func TestOldLossesDoNotCount(t *testing.T) {
	f := &fakeEngine{equity: d("10000"), seq: 9, closes: []trading.ClosedTrade{{Net: d("-20")}, {Net: d("-20")}, {Net: d("-20")}, {Net: d("-20")}}}
	m := New(f, nil)
	m.Now = func() time.Time { return noon }
	m.Register(1, 100)
	m.Tick()
	if v := m.View(1).Accounts[0]; v.Streak != 0 || v.Lock != nil {
		t.Fatalf("以前的亏损不算：%+v", v)
	}
}

// 已归档的账户只留在事件里，不再检查
func TestArchivedNotChecked(t *testing.T) {
	r := newRig(t, "10000")
	evs := []Event{{Seq: 1, Kind: "day", Account: 55, Day: 1, Open: func() *decimal.Decimal { v := d("10000"); return &v }()}}
	r.m.Restore(evs)
	r.f.equity = d("1")
	r.m.check(55)
	if h := r.m.history[55]; len(h) != 0 {
		t.Fatalf("不该再检查：%+v", h)
	}
}

// 4.14 手算：权益 10,000、上限 1% = 100。76,000 买 0.05，止损 74,000：最多亏 100，不拦；0.051 拦
func TestPerTradeByHand(t *testing.T) {
	r := newRig(t, "10000")
	if err := r.m.Guard(open(d("76000"), "0.05", "74000")); err != nil {
		t.Fatalf("正好 100 不拦：%v", err)
	}
	err := r.m.Guard(open(d("76000"), "0.051", "74000"))
	rj, ok := err.(*trading.Reject)
	if !ok || rj.Code != "DISCIPLINE_RISK" || rj.Params["risk"] != "102" || rj.Params["limit"] != "100" {
		t.Fatalf("应拒单并写明风险 102、上限 100：%+v", err)
	}
	// 没带止损不拦（D80）
	if err := r.m.Guard(open(d("76000"), "10", "0")); err != nil {
		t.Fatalf("没带止损不拦：%v", err)
	}
	// AI 的单不查每笔风险
	p := open(d("76000"), "10", "74000")
	p.Source = "follow"
	if err := r.m.Guard(p); err != nil {
		t.Fatalf("AI 的单不查每笔风险：%v", err)
	}
	// 系统账户不管
	p = open(d("76000"), "10", "74000")
	p.Account = -1
	if err := r.m.Guard(p); err != nil {
		t.Fatalf("系统账户不管：%v", err)
	}
}

// 4.18 锁定时开仓一律拒绝（手动、AI、条件单），写明规则和解开时间
func TestLockedRejectsAll(t *testing.T) {
	r := newRig(t, "10000")
	r.eq("9000")
	r.wantLock(RuleDaily)
	for _, src := range []string{"manual", "follow", "cond"} {
		p := open(d("76000"), "0.001", "0")
		p.Source = src
		rj, ok := r.m.Guard(p).(*trading.Reject)
		if !ok || rj.Code != "DISCIPLINE_LOCKED" || rj.Params["rule"] != RuleDaily || rj.Params["until"] == "" {
			t.Fatalf("%s 应被拒：%+v", src, rj)
		}
	}
	if err := r.m.SetRules(1, func() Rules { x := Default(); x.DailyLoss = d("0.04"); return x }()); err != nil {
		t.Fatal(err)
	}
	hist, err := r.m.History(1, 100)
	if err != nil || len(hist) < 5 {
		t.Fatalf("拒单、锁定、改规则都要留记录：%d 条，%v", len(hist), err)
	}
	if hist[0].Kind != "settings" || hist[len(hist)-1].Kind != "lock" {
		t.Fatalf("新的在前：%s … %s", hist[0].Kind, hist[len(hist)-1].Kind)
	}
}

// 4.19 解锁：等 10 分钟，当天只能一次；解开后这条规则当天不再查
func TestUnlockOncePerDay(t *testing.T) {
	r := newRig(t, "10000")
	r.eq("9000")
	r.wantLock(RuleDaily)
	if err := r.m.RequestUnlock(1, 100); err != nil {
		t.Fatal(err)
	}
	if err := r.m.CanUnlock(1, 100); err == nil || err.(*Err).Code != "UNLOCK_PENDING" {
		t.Fatalf("等待中不能再申请：%v", err)
	}
	// 等待期间还是拒单
	if err := r.m.Guard(open(d("76000"), "0.001", "0")); err == nil {
		t.Fatal("等待期间还是锁定的")
	}
	r.now = r.now.Add(UnlockWait)
	r.m.Tick()
	r.wantFree()
	if err := r.m.Guard(open(d("76000"), "0.001", "0")); err != nil {
		t.Fatalf("解开后能下单：%v", err)
	}
	// 单日亏损今天不再查
	r.eq("8600")
	r.wantFree()
	// 换一条规则锁住，当天不能再解
	r.f.seq++
	r.f.closes = []trading.ClosedTrade{{OrderID: "a", Instrument: "BTC-PERP", Net: d("-20")}, {OrderID: "b", Instrument: "BTC-PERP", Net: d("-20")}, {OrderID: "c", Instrument: "BTC-PERP", Net: d("-20")}}
	r.m.Tick()
	r.wantLock(RuleStreak)
	if err := r.m.CanUnlock(1, 100); err == nil || err.(*Err).Code != "UNLOCK_USED" {
		t.Fatalf("当天只能解一次：%v", err)
	}
	// 别人的账户
	if err := r.m.CanUnlock(2, 100); err == nil || err.(*Err).HTTP != 404 {
		t.Fatalf("别人的账户 404：%v", err)
	}
}

// 4.11 自己打开时基准重新定（关着期间的亏损不算）；锁定中不能关。关着的时候不查规则（D107 起登录不再自动打开）
func TestEnabledAndLogin(t *testing.T) {
	r := newRig(t, "10000")
	if err := r.m.SetEnabled(1, false); err != nil {
		t.Fatal(err)
	}
	r.eq("5000")
	r.wantFree()
	if err := r.m.Guard(open(d("76000"), "10", "74000")); err != nil {
		t.Fatalf("关着时不查：%v", err)
	}
	if err := r.m.SetEnabled(1, true); err != nil || !r.m.View(1).Enabled {
		t.Fatalf("打开：%v", err)
	}
	r.eq("5000")
	r.wantFree() // 基准重置了，关闭期间的亏损不算
	if v := r.m.View(1).Accounts[0]; !v.DayOpen.Equal(d("5000")) || !v.Peak.Equal(d("5000")) {
		t.Fatalf("基准应重置为 5000：%+v", v)
	}
	r.eq("4750")
	r.wantLock(RuleDaily)
	if err := r.m.SetEnabled(1, false); err == nil || err.(*Err).Code != "DISCIPLINE_IS_LOCKED" {
		t.Fatalf("锁定中不能关：%v", err)
	}
}

// 4.10、4.12 规则范围；放宽要密码，收紧不用；锁定中不能放宽
func TestRules(t *testing.T) {
	r := newRig(t, "10000")
	bad := Default()
	bad.DailyLoss = d("0.9")
	if err := r.m.SetRules(1, bad); err == nil {
		t.Fatal("单日亏损 90% 超出范围")
	}
	bad = Default()
	bad.StopR = d("3")
	if err := r.m.SetRules(1, bad); err == nil {
		t.Fatal("回落线不能高于盈利线")
	}
	tight := Default()
	tight.DailyLoss = d("0.03")
	if r.m.NeedsPassword(1, tight) {
		t.Fatal("收紧不要密码")
	}
	loose := Default()
	loose.DailyLoss = d("0.1")
	if !r.m.NeedsPassword(1, loose) {
		t.Fatal("放宽要密码")
	}
	if err := r.m.SetRules(1, tight); err != nil {
		t.Fatal(err)
	}
	r.eq("9700")
	r.wantLock(RuleDaily)
	if err := r.m.SetRules(1, Default()); err == nil || err.(*Err).Code != "DISCIPLINE_IS_LOCKED" {
		t.Fatalf("锁定中不能放宽：%v", err)
	}
	tighter := tight
	tighter.DailyLoss = d("0.02")
	if err := r.m.SetRules(1, tighter); err != nil {
		t.Fatalf("锁定中可以收紧：%v", err)
	}
}

// 4.20 只追加事件：重放后状态一样
func TestReplay(t *testing.T) {
	r := newRig(t, "10000")
	var all []Event
	r.m.Persist = func(b []Event) error { all = append(all, b...); return nil }
	r.eq("10200")
	r.eq("9400")
	r.wantLock(RuleDaily)
	if err := r.m.RequestUnlock(1, 100); err != nil {
		t.Fatal(err)
	}
	r.m.Flush()
	// 开启 Persist 之前的事件这里重放不到，所以换一个从头记录的
	f := &fakeEngine{equity: d("10000")}
	a := New(f, nil)
	now := noon
	a.Now = func() time.Time { return now }
	var evs []Event
	a.Persist = func(b []Event) error { evs = append(evs, b...); return nil }
	a.Register(1, 100)
	a.SetTZ(1, 480)
	a.Tick()
	f.equity = d("10200")
	a.Tick()
	f.equity = d("9400")
	a.Tick()
	if err := a.RequestUnlock(1, 100); err != nil {
		t.Fatal(err)
	}
	a.Flush()
	b := New(&fakeEngine{equity: d("9400")}, nil)
	b.Now = a.Now
	b.Restore(evs)
	b.Register(1, 100)
	va, vb := a.View(1), b.View(1)
	if len(vb.Accounts) != 1 || vb.Accounts[0].Lock == nil || vb.Accounts[0].Lock.UnlockAt != va.Accounts[0].Lock.UnlockAt ||
		!vb.Accounts[0].DayOpen.Equal(va.Accounts[0].DayOpen) || !vb.Accounts[0].UnlockUsed || vb.TZ != 480 {
		t.Fatalf("重放后不一样：\n%+v\n%+v", va.Accounts[0], vb.Accounts[0])
	}
}

// D107：新用户的纪律锁默认关着，登录也不会自动打开；老用户（没动过开关）保持原来的"开"
func TestNewUserStartsOff(t *testing.T) {
	r := newRig(t, "10000") // 用户 1 是"老用户"：没有任何开关记录
	if !r.m.View(1).Enabled {
		t.Fatal("老用户保持开着")
	}
	r.m.NewUser(2)
	r.m.Register(2, 200)
	if r.m.View(2).Enabled {
		t.Fatal("新用户默认关着")
	}
	big := open(d("76000"), "10", "74000") // 每笔风险远超 1%
	if err := r.m.Guard(big); err == nil {
		t.Fatal("老用户照常查，这笔该拦")
	}
	big.Account = 200
	if err := r.m.Guard(big); err != nil {
		t.Fatalf("新用户没打开，不查：%v", err)
	}
}

// D107：连亏只数亏得比"权益 × 门槛"多的单（默认 0.1%）；比它小的盈亏当没发生，不算一笔、也不打断
func TestSmallLossesDontCount(t *testing.T) {
	r := newRig(t, "10000") // 门槛 10 USDT
	close := func(net string) {
		r.f.seq++
		r.f.closes = []trading.ClosedTrade{{OrderID: "o", Instrument: "BTC-PERP", Net: d(net)}}
		r.m.Tick()
	}
	for i := 0; i < 6; i++ {
		close("-0.5") // 试单、手续费这种几毛钱的亏损
	}
	r.wantFree()
	close("-30")
	close("-30")
	close("+2") // 小赚也不打断
	r.wantFree()
	close("-30")
	r.wantLock(RuleStreak)
	// 门槛设 0：每一笔都算（D107 以前的规则）
	r2 := newRig(t, "10000")
	rules := Default()
	rules.MinLoss = decimal.Zero
	if err := r2.m.SetRules(1, rules); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r2.f.seq++
		r2.f.closes = []trading.ClosedTrade{{OrderID: "o", Instrument: "BTC-PERP", Net: d("-0.5")}}
		r2.m.Tick()
	}
	r2.wantLock(RuleStreak)
	// 门槛调高算放宽（要二级密码），调低算收紧
	looser := Default()
	looser.MinLoss = d("0.01")
	if !looser.LooserThan(Default()) || Default().LooserThan(looser) {
		t.Fatal("门槛调高是放宽")
	}
}
