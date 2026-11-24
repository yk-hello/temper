package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

func newSvc() (*Service, *int64) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := int64(1_000_000_000)
	s.Now = func() int64 { return now }
	return s, &now
}

func kinds(t *testing.T, s *Service, user int64) []string {
	t.Helper()
	l, _, err := s.List(context.Background(), user, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, it := range l {
		out = append(out, it.Kind)
	}
	return out
}

func TestDefaultsAndSwitches(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	// 默认开：安全类、机器人、到价、到期；默认关：成交、大户、经济事件（D85 第 4 条）
	s.Push(ctx, 1, KLiq, nil)
	s.Push(ctx, 1, KFill, nil)
	s.Push(ctx, 1, KWhale, nil)
	s.Push(ctx, 1, KEcon, nil)
	if got := kinds(t, s, 1); len(got) != 1 || got[0] != KLiq {
		t.Fatalf("默认开关不对：%v", got)
	}
	// 关掉站内就不写
	_ = s.SetPref(ctx, 1, KLiq, Pref{})
	s.Push(ctx, 1, KLiq, nil)
	if got := kinds(t, s, 1); len(got) != 1 {
		t.Fatalf("关了还写：%v", got)
	}
	if err := s.SetPref(ctx, 1, "nope", Pref{}); err == nil {
		t.Fatal("不认识的事件应该拒绝")
	}
	// 未读和标记已读
	if _, n, _ := s.List(ctx, 1, 10); n != 1 {
		t.Fatalf("未读 %d", n)
	}
	_ = s.MarkRead(ctx, 1)
	if _, n, _ := s.List(ctx, 1, 10); n != 0 {
		t.Fatalf("已读后还有 %d", n)
	}
	// 别人的看不到
	if got := kinds(t, s, 2); len(got) != 0 {
		t.Fatalf("串号：%v", got)
	}
}

// fakeTG 一个假的 Telegram 服务器。
type fakeTG struct {
	mu      sync.Mutex
	sent    []string
	updates []map[string]any
	fail    int // 前几次 sendMessage 失败
	srv     *httptest.Server
}

func newFake(t *testing.T) *fakeTG {
	f := &fakeTG{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"username":"TestBot"}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			ups := f.updates
			f.updates = nil
			if len(ups) == 0 {
				time.Sleep(5 * time.Millisecond)
			}
			b, _ := json.Marshal(map[string]any{"ok": true, "result": ups})
			_, _ = w.Write(b)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			if f.fail > 0 {
				f.fail--
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"ok":false,"parameters":{"retry_after":0}}`)
				return
			}
			f.sent = append(f.sent, body["text"].(string))
			_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTG) msg(id, chat int64, kind, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"update_id": id, "message": map[string]any{"text": text, "chat": map[string]any{"id": chat, "type": kind}}})
}

func (f *fakeTG) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

func TestTelegramFlow(t *testing.T) {
	s, _ := newSvc()
	f := newFake(t)
	tg := &Telegram{Token: "SECRET", Base: f.srv.URL, S: s, Retry: []time.Duration{0, time.Millisecond}}
	s.TG = tg
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tg.Run(ctx)
	waitFor(t, "bot 名字", func() bool { return tg.Bot() == "TestBot" })
	linked := func(u int64) int64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.links[u]
	}

	// 群里发绑定码不算：通知里有账户信息，只发私聊
	code := tg.NewCode(7)
	f.msg(1, -55, "group", "/start "+code)
	// 错的码
	f.msg(2, 100, "private", "/start deadbeef")
	waitFor(t, "错码提示", func() bool { return len(f.texts()) == 1 })
	if !strings.Contains(f.texts()[0], "失效") {
		t.Fatalf("提示：%v", f.texts())
	}
	if linked(7) != 0 {
		t.Fatal("不该绑定")
	}
	// 对的码：绑定，只能用一次
	f.msg(3, 100, "private", "/start "+code)
	waitFor(t, "绑定", func() bool { return linked(7) == 100 })
	f.msg(4, 200, "private", "/start "+code)
	waitFor(t, "二次使用被拒", func() bool { return len(f.texts()) >= 3 })
	if linked(7) != 100 {
		t.Fatal("一次性代码被用了两次")
	}

	// 没有套餐之分（D90）：打开了的事件都发 Telegram
	base := len(f.texts())
	s.Push(ctx, 7, KLiq, map[string]string{"inst": "BTC-PERP", "px": "90000", "dist": "4.2"})
	waitFor(t, "强平预警到达", func() bool { return len(f.texts()) == base+1 })
	if got := f.texts(); !strings.Contains(got[base], "强平预警") {
		t.Fatalf("强平预警：%v", got[base:])
	}
	s.Push(ctx, 7, KBot, map[string]string{"inst": "ETH-PERP"})
	waitFor(t, "机器人通知", func() bool { return len(f.texts()) == base+2 })

	// 被限流一次：重试，不丢
	f.mu.Lock()
	f.fail = 1
	f.mu.Unlock()
	s.Push(ctx, 7, KMargin, map[string]string{"ratio": "72", "acct": "A"})
	waitFor(t, "重试后到达", func() bool { return len(f.texts()) == base+3 })

	// /stop 解绑，之后不再发
	f.msg(5, 100, "private", "/stop")
	waitFor(t, "解绑", func() bool { return linked(7) == 0 })
}

// 一个 Telegram 绑了几个账号：/stop 要全部解开，只解一个的话另一个还会继续收到。
func TestStopUnlinksEveryAccount(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	s.bind(ctx, 1, 100)
	s.bind(ctx, 2, 100)
	s.bind(ctx, 3, 200)
	s.unbindChat(ctx, 100)
	if s.links[1] != 0 || s.links[2] != 0 {
		t.Fatalf("没解干净：%v", s.links)
	}
	if s.links[3] != 200 {
		t.Fatal("解到别人了")
	}
}

func TestTokenNeverInError(t *testing.T) {
	tg := &Telegram{Token: "SECRET-TOKEN", Base: "http://127.0.0.1:1"}
	err := tg.call(context.Background(), "getMe", map[string]any{}, nil)
	if err == nil {
		t.Fatal("应该连不上")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("token 进了错误信息：%v", err)
	}
}

func TestDisabledWithoutToken(t *testing.T) {
	tg := &Telegram{}
	if tg.Enabled() {
		t.Fatal("没 token 不能算启用")
	}
	tg.Send(1, "x") // 不能卡住
	var nilTG *Telegram
	if nilTG.Enabled() {
		t.Fatal("nil")
	}
}

type world struct {
	snap    map[int64]*Snap
	fills   []FillSnap
	bots    []BotSnap
	follows []FollowSnap
	whales  []WhaleSnap
	econ    []EconSnap
	px      float64
}

func rig() (*Service, *Watcher, *world, *int64) {
	s, now := newSvc()
	wd := &world{snap: map[int64]*Snap{1: {}}, px: 100}
	w := &Watcher{S: s, Src: Sources{
		Accounts: func(context.Context) []AcctRef { return []AcctRef{{ID: 1, User: 9, Name: "A"}} },
		Snap:     func(a int64) (*Snap, bool) { v, ok := wd.snap[a]; return v, ok },
		FillSeq: func(int64) int64 {
			if len(wd.fills) == 0 {
				return 0
			}
			return wd.fills[len(wd.fills)-1].Seq
		},
		Fills: func(_, after int64) []FillSnap {
			out := []FillSnap{}
			for _, f := range wd.fills {
				if f.Seq > after {
					out = append(out, f)
				}
			}
			return out
		},
		Bots:    func(int64) []BotSnap { return wd.bots },
		Follows: func(int64) []FollowSnap { return wd.follows },
		Price:   func(string) float64 { return wd.px },
		Whales:  func() []WhaleSnap { return wd.whales },
		Econ:    func() []EconSnap { return wd.econ },
	}}
	return s, w, wd, now
}

// tickN 跑几轮，让按人看、大户、日历也轮到。
func tickN(w *Watcher, n int) {
	for i := 0; i < n; i++ {
		w.Tick(context.Background())
	}
}

func count(t *testing.T, s *Service, user int64, kind string) int {
	n := 0
	for _, k := range kinds(t, s, user) {
		if k == kind {
			n++
		}
	}
	return n
}

func TestNoReplayAfterRestart(t *testing.T) {
	s, w, wd, _ := rig()
	wd.snap[1] = &Snap{Notices: []NoticeSnap{{Seq: 5, Kind: "liquidation", Inst: "BTC-PERP", Px: 1, Loss: 2}}, Locked: true}
	wd.bots = []BotSnap{{ID: 1, Inst: "BTC-PERP", Paused: true, Reason: "INSUFFICIENT_BALANCE"}}
	wd.follows = []FollowSnap{{ID: 1, Stopped: true, Reason: "max_loss"}}
	tickN(w, 4)
	if got := kinds(t, s, 9); len(got) != 0 {
		t.Fatalf("重启后把旧事又报了一遍：%v", got)
	}
	// 新的事件才报（止损默认关，这里打开）
	_ = s.SetPref(context.Background(), 9, KStop, Pref{Site: true})
	wd.snap[1].Notices = append(wd.snap[1].Notices, NoticeSnap{Seq: 6, Kind: "sl", Inst: "BTC-PERP", Px: 10})
	tickN(w, 1)
	if count(t, s, 9, KStop) != 1 {
		t.Fatalf("止损没报：%v", kinds(t, s, 9))
	}
	tickN(w, 3)
	if count(t, s, 9, KStop) != 1 {
		t.Fatal("同一事件报了两次")
	}
}

func TestNewAccountStartsFromZero(t *testing.T) {
	s, w, wd, _ := rig()
	_ = s.SetPref(context.Background(), 9, KStop, Pref{Site: true})
	tickN(w, 2)
	// 启动之后才开的账户，第一眼之前发生的也要报
	w.Src.Accounts = func(context.Context) []AcctRef {
		return []AcctRef{{ID: 1, User: 9, Name: "A"}, {ID: 2, User: 9, Name: "B"}}
	}
	wd.snap[2] = &Snap{Notices: []NoticeSnap{{Seq: 1, Kind: "tp", Inst: "BTC-PERP", Px: 10}}}
	w.list = nil
	tickN(w, 1)
	if count(t, s, 9, KStop) != 1 {
		t.Fatalf("新账户的事件：%v", kinds(t, s, 9))
	}
}

func TestStillDangerousAfterRestart(t *testing.T) {
	s, w, wd, now := rig()
	wd.snap[1] = &Snap{Ratio: 0.9, Positions: []PosSnap{{Inst: "BTC-PERP", Mark: 100, Liq: 98}}}
	tickN(w, 1)
	if count(t, s, 9, KLiq) != 1 || count(t, s, 9, KMargin) != 1 {
		t.Fatalf("重启时已经危险，没响过要响：%v", kinds(t, s, 9))
	}
	// 再重启一次，一小时内不重复
	w2 := &Watcher{S: s, Src: w.Src}
	tickN(w2, 1)
	if count(t, s, 9, KLiq) != 1 || count(t, s, 9, KMargin) != 1 {
		t.Fatalf("一小时内重启不该再响：%v", kinds(t, s, 9))
	}
	*now += refireMs + 1
	w3 := &Watcher{S: s, Src: w.Src}
	tickN(w3, 1)
	if count(t, s, 9, KLiq) != 2 {
		t.Fatalf("过了一小时还危险应该再响：%v", kinds(t, s, 9))
	}
}

// 全仓强平没有单一价格：不能写成"在 0 强平"。
func TestCrossLiquidationHasNoPrice(t *testing.T) {
	s, w, wd, _ := rig()
	tickN(w, 1)
	wd.snap[1].Notices = []NoticeSnap{{Seq: 9, Kind: "liquidation", Inst: "BTC-PERP", Loss: 88.5}}
	tickN(w, 1)
	l, _, _ := s.List(context.Background(), 9, 5)
	if len(l) != 1 || l[0].Params["px"] != "" || l[0].Params["loss"] != "88.50" {
		t.Fatalf("%+v", l)
	}
	for _, lang := range []string{"zh", "en"} {
		if txt := Render(lang, KLiq, l[0].Params); strings.Contains(txt, " 0") || !strings.Contains(txt, "88.50") {
			t.Fatalf("%s：%s", lang, txt)
		}
	}
}

func TestHysteresis(t *testing.T) {
	s, w, wd, _ := rig()
	tickN(w, 1)
	set := func(mark float64) {
		wd.snap[1].Positions = []PosSnap{{Inst: "BTC-PERP", Mark: mark, Liq: 95}}
	}
	set(99) // 距离 4%
	tickN(w, 1)
	if count(t, s, 9, KLiq) != 1 {
		t.Fatal("没响")
	}
	set(101) // 5.9%：没到 7.5%，不重置
	tickN(w, 1)
	set(99.5)
	tickN(w, 1)
	if count(t, s, 9, KLiq) != 1 {
		t.Fatalf("在线附近来回不该再响：%d", count(t, s, 9, KLiq))
	}
	set(110) // 13.6%：重置
	tickN(w, 1)
	set(99)
	tickN(w, 1)
	if count(t, s, 9, KLiq) != 2 {
		t.Fatalf("重置后应该再响：%d", count(t, s, 9, KLiq))
	}
	// 平仓后再开，重新算
	wd.snap[1].Positions = nil
	tickN(w, 1)
	set(99)
	tickN(w, 1)
	if count(t, s, 9, KLiq) != 3 {
		t.Fatalf("重新开仓：%d", count(t, s, 9, KLiq))
	}
	// 保证金率同理
	wd.snap[1].Ratio = 0.71
	tickN(w, 1)
	wd.snap[1].Ratio = 0.60
	tickN(w, 1)
	wd.snap[1].Ratio = 0.71
	tickN(w, 1)
	if count(t, s, 9, KMargin) != 1 {
		t.Fatalf("保证金率来回：%d", count(t, s, 9, KMargin))
	}
	wd.snap[1].Ratio = 0.50
	tickN(w, 1)
	wd.snap[1].Ratio = 0.71
	tickN(w, 1)
	if count(t, s, 9, KMargin) != 2 {
		t.Fatalf("保证金率重置：%d", count(t, s, 9, KMargin))
	}
	// 没有强平价（现货、没仓位）不算
	wd.snap[1].Positions = []PosSnap{{Inst: "X", Mark: 100, Liq: 0}}
	before := count(t, s, 9, KLiq)
	tickN(w, 1)
	if count(t, s, 9, KLiq) != before {
		t.Fatal("没有强平价也响了")
	}
}

func TestLockBotFollow(t *testing.T) {
	s, w, wd, _ := rig()
	tickN(w, 1)
	wd.snap[1].Locked = true
	tickN(w, 2)
	if count(t, s, 9, KLock) != 1 {
		t.Fatal("纪律锁")
	}
	wd.snap[1].Locked = false
	tickN(w, 1)
	wd.snap[1].Locked = true
	tickN(w, 1)
	if count(t, s, 9, KLock) != 2 {
		t.Fatal("解开后再锁应该再报")
	}
	// 自己点的暂停（USER）不提醒；出错的、手动单成交让机器人让路的（MANUAL）各提醒一次
	wd.bots = []BotSnap{{ID: 1, Inst: "A", Paused: true, Reason: "USER"}, {ID: 2, Inst: "B", Paused: true, Reason: "INSUFFICIENT_BALANCE"}, {ID: 3, Inst: "C", Paused: true, Reason: "MANUAL"}}
	tickN(w, 6)
	if count(t, s, 9, KBot) != 2 {
		t.Fatalf("机器人：%d", count(t, s, 9, KBot))
	}
	// 用户自己停的跟随不提醒，亏到上限的提醒（这项默认关，这里打开）
	_ = s.SetPref(context.Background(), 9, KFollow, Pref{Site: true})
	wd.follows = []FollowSnap{{ID: 1, Stopped: true, Reason: "user"}, {ID: 2, Stopped: true, Reason: "max_loss"}}
	tickN(w, 6)
	if count(t, s, 9, KFollow) != 1 {
		t.Fatalf("跟随：%d", count(t, s, 9, KFollow))
	}
}

func TestFillsOnlyWhenOn(t *testing.T) {
	s, w, wd, _ := rig()
	ctx := context.Background()
	wd.fills = []FillSnap{{Seq: 1, Inst: "BTC-PERP", Side: "buy", Qty: 1, Px: 100}}
	tickN(w, 1)
	wd.fills = append(wd.fills, FillSnap{Seq: 2, Inst: "BTC-PERP", Side: "buy", Qty: 1, Px: 100})
	tickN(w, 1)
	if count(t, s, 9, KFill) != 0 {
		t.Fatal("默认关的还报了")
	}
	// 打开后不补发旧的
	_ = s.SetPref(ctx, 9, KFill, Pref{Site: true})
	tickN(w, 1)
	if count(t, s, 9, KFill) != 0 {
		t.Fatal("打开后补发了旧成交")
	}
	wd.fills = append(wd.fills, FillSnap{Seq: 3, Inst: "BTC-PERP", Side: "sell", Qty: 1, Px: 100}, FillSnap{Seq: 4, Inst: "BTC-PERP", Side: "sell", Qty: 1, Px: 100, Liq: true})
	tickN(w, 2)
	if count(t, s, 9, KFill) != 1 {
		t.Fatalf("新成交：%d，强平单独报过不该算", count(t, s, 9, KFill))
	}
}

func TestPriceAlerts(t *testing.T) {
	s, w, wd, _ := rig()
	ctx := context.Background()
	if _, err := s.AddAlert(ctx, 9, "BTC-PERP", 0, 100); err == nil {
		t.Fatal("0 应该拒绝")
	}
	if _, err := s.AddAlert(ctx, 9, "BTC-PERP", 100, 100); err == nil {
		t.Fatal("等于现价应该拒绝")
	}
	up, _ := s.AddAlert(ctx, 9, "BTC-PERP", 110, 100)
	down, _ := s.AddAlert(ctx, 9, "BTC-PERP", 90, 100)
	other, _ := s.AddAlert(ctx, 3, "BTC-PERP", 120, 100)
	if !up.Up || down.Up {
		t.Fatal("方向")
	}
	if s.DelAlert(ctx, 9, other.ID) {
		t.Fatal("不能删别人的")
	}
	tickN(w, 1)
	if count(t, s, 9, KPrice) != 0 {
		t.Fatal("没到价就响了")
	}
	wd.px = 111
	tickN(w, 2)
	if count(t, s, 9, KPrice) != 1 {
		t.Fatalf("涨到：%d", count(t, s, 9, KPrice))
	}
	wd.px = 89
	tickN(w, 2)
	if count(t, s, 9, KPrice) != 2 {
		t.Fatal("跌到")
	}
	if len(s.Alerts(9)) != 0 {
		t.Fatal("触发后应该清掉")
	}
	wd.px = 0 // 取不到价
	x, _ := s.AddAlert(ctx, 9, "BTC-PERP", 50, 100)
	tickN(w, 2)
	if len(s.Alerts(9)) != 1 || x == nil {
		t.Fatal("取不到价不能算触发")
	}
	for i := 0; i < MaxAlerts; i++ {
		_, _ = s.AddAlert(ctx, 77, "BTC-PERP", float64(200+i), 100)
	}
	if _, err := s.AddAlert(ctx, 77, "BTC-PERP", 999, 100); err == nil {
		t.Fatal("超过上限")
	}
}

func TestBroadcasts(t *testing.T) {
	s, w, wd, now := rig()
	ctx := context.Background()
	wd.whales = []WhaleSnap{{T: 5, Coin: "BTC", Kind: "open", Value: 9e6}}
	tickN(w, 1) // 启动时的旧事件不报
	_ = s.SetPref(ctx, 9, KWhale, Pref{Site: true})
	_ = s.SetPref(ctx, 9, KEcon, Pref{Site: true})
	_ = s.SetPref(ctx, 8, KEcon, Pref{Site: true})
	wd.whales = []WhaleSnap{{T: 10 * 60 * 1000, Coin: "BTC", Kind: "add", Value: 9e6}, {T: 10*60*1000 - 1000, Coin: "BTC", Kind: "add", Value: 9e6}}
	tickN(w, 15)
	if c := count(t, s, 9, KWhale); c != 1 {
		t.Fatalf("同一币种五分钟一条：%d", c)
	}
	if count(t, s, 8, KWhale) != 0 {
		t.Fatal("没打开的人不该收到")
	}
	wd.econ = []EconSnap{{T: *now + EconAheadMs - 1, Key: "cpi"}, {T: *now + 3*EconAheadMs, Key: "nfp"}, {T: *now - 1, Key: "ppi"}}
	tickN(w, 60)
	if count(t, s, 9, KEcon) != 1 || count(t, s, 8, KEcon) != 1 {
		t.Fatalf("经济事件只响一次：%d %d", count(t, s, 9, KEcon), count(t, s, 8, KEcon))
	}
}

func TestDeleteUser(t *testing.T) {
	s, _ := newSvc()
	ctx := context.Background()
	s.Push(ctx, 4, KLiq, nil)
	_, _ = s.AddAlert(ctx, 4, "X", 1, 2)
	_ = s.SetPref(ctx, 4, KWhale, Pref{Site: true})
	s.bind(ctx, 4, 100)
	s.DeleteUser(ctx, 4)
	if len(kinds(t, s, 4)) != 0 || len(s.Alerts(4)) != 0 || len(s.Wants(KWhale)) != 0 || s.links[4] != 0 {
		t.Fatal("删账户要删干净")
	}
}

// 英文通知不能混中文，中文通知不能漏参数。
func TestRender(t *testing.T) {
	params := []struct {
		kind string
		p    map[string]string
	}{
		{KLiq, map[string]string{"inst": "BTC-PERP", "px": "90000.0", "dist": "4.2"}},
		{KLiq, map[string]string{"inst": "BTC-PERP", "px": "90000.0", "loss": "120.00", "done": "1"}},
		{KLiq, map[string]string{"loss": "120.00", "done": "1", "acct": "模拟账户 1"}},
		{KMargin, map[string]string{"ratio": "72", "acct": "模拟账户 1"}},
		{KLock, nil},
		{KBot, map[string]string{"inst": "ETH-PERP"}},
		{KPrice, map[string]string{"inst": "BTC-PERP", "px": "100000", "dir": "up"}},
		{KPrice, map[string]string{"inst": "BTC-PERP", "px": "100000", "dir": "down"}},
		{KRule, map[string]string{"inst": "BTC-PERP", "name": "Funding hot", "conds": "funding>0.05=0.0612;chg24h>5=6.3", "join": "all"}},
		{KFill, map[string]string{"inst": "BTC-PERP", "side": "buy", "qty": "1", "px": "1"}},
		{KStop, map[string]string{"inst": "BTC-PERP", "what": "tp", "px": "1"}},
		{KStop, map[string]string{"inst": "BTC-PERP", "what": "sl", "px": "1"}},
		{KFollow, nil},
		{KWhale, map[string]string{"coin": "BTC", "what": "open", "value": "9.0M"}},
		{KEcon, map[string]string{"key": "nfp"}},
		{KHigh, map[string]string{"inst": "BTC-PERP", "side": "short", "roe": "24.5", "lev": "10"}},
		{KLogin, map[string]string{"device": "Edge · Windows", "ip": "203.0.113.5"}},
		{KRecon, map[string]string{"what": "external", "account": "Main", "inst": "BTC-PERP", "side": "buy", "qty": "0.01", "px": "60000"}},
		{KRecon, map[string]string{"what": "liq", "account": "Main", "inst": "ETH-PERP", "side": "sell", "qty": "1", "px": "3000"}},
		{KRecon, map[string]string{"what": "adl", "account": "Main", "inst": "ETH-PERP", "side": "buy", "qty": "1", "px": "3000"}},
		{KRecon, map[string]string{"what": "money", "account": "Main", "amount": "500.00", "transfer": "500.00"}},
		{KRecon, map[string]string{"what": "money", "account": "Main", "amount": "-12.00", "insurance": "-10.00", "rest": "-2.00"}},
		{KRecon, map[string]string{"what": "money", "account": "Main", "amount": "3.00"}},
		{KRecon, map[string]string{"what": "fail", "account": "Main", "err": "-2015 Invalid API-key, IP, or permissions for action."}},
		{KRecon, map[string]string{"what": "ok", "account": "Main"}},
		{KStrat, map[string]string{"strategy": "my-strat", "targets": "BTC-PERP:0.3:0.7,ETH-PERP:-0.1:0.55,SOL-PERP:0:0", "more": "2", "reason": "均线金叉", "reason_en": "MA cross"}},
		{KStrat, map[string]string{"strategy": "my-strat", "targets": "BTC-PERP:0.25"}},
		{KRecap, map[string]string{"acct": "Paper 1", "day": "2026-11-11", "closed": "3", "wins": "2", "losses": "1", "realized": "12.30", "fees": "1.20", "funding": "-0.10", "net": "11.00", "fills": "8"}},
	}
	seen := map[string]bool{}
	for _, c := range params {
		seen[c.kind] = true
		en, zh := Render("en", c.kind, c.p), Render("zh", c.kind, c.p)
		for _, r := range en {
			if unicode.Is(unicode.Han, r) || r > 0x2E7F {
				t.Fatalf("英文混了中文或全角：%s", en)
			}
		}
		if en == c.kind || zh == c.kind || strings.Contains(en, "%!") || strings.Contains(zh, "%!") {
			t.Fatalf("没拼好：%q %q", en, zh)
		}
		if !strings.ContainsFunc(zh, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
			t.Fatalf("中文模板：%s", zh)
		}
	}
	for _, k := range Kinds {
		if !seen[k] {
			t.Fatalf("%s 没测", k)
		}
	}
}

// 通知文字里的数字和网页上一样写（11-17 走查）
func TestNumFormat(t *testing.T) {
	for in, want := range map[string]string{
		"64103.6": "64,103.6", "-0.25": "−0.25", "+1234567.891": "+1,234,567.891", "0.00": "0.00",
		"999": "999", "1000": "1,000", "": "", "abc": "abc", "1.2.3": "1.2.3", "-": "-",
	} {
		if got := num(in); got != want {
			t.Errorf("num(%q) = %q，应为 %q", in, got, want)
		}
	}
	if got := signed("12345.5"); got != "+12,345.5" {
		t.Errorf("signed = %q", got)
	}
}
