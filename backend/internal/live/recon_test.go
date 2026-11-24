package live

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 对账差异告警（18 第 6.4 条）：从这里下的单不提醒；别处下的单、划转、对账失败和恢复各提醒一次。
func TestReconAlerts(t *testing.T) {
	reconTick.set(300 * time.Millisecond)
	suspectWait.set(200 * time.Millisecond)
	failAfter.set(600 * time.Millisecond)
	t.Cleanup(func() {
		reconTick.set(15 * time.Second)
		suspectWait.set(2 * time.Second)
		failAfter.set(2 * time.Minute)
	})
	e := newEnv(t)
	var mu sync.Mutex
	var got []map[string]string
	e.desks.OnAlert = func(_ int64, p map[string]string) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}
	count := func(what string) (n int, last map[string]string) {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range got {
			if p["what"] == what {
				n, last = n+1, p
			}
		}
		return
	}
	wait := func(what string) map[string]string {
		for i := 0; i < 100; i++ {
			if n, p := count(what); n > 0 {
				return p
			}
			time.Sleep(100 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("等不到 %s 提醒：%v", what, got)
		return nil
	}

	a := e.user("carol@example.com")
	st, r := a.do("POST", "/api/live/keys", map[string]any{"env": "fake", "label": "Main", "key": fKey, "secret": fSecret, "trade_password": "trade-pw-8899"})
	if st != 200 {
		t.Fatalf("密钥：%d %v", st, r)
	}
	st, r = a.do("POST", "/api/live/accounts", map[string]any{"key_id": r["key"].(map[string]any)["id"], "name": "Main"})
	if st != 200 {
		t.Fatalf("账户：%d %v", st, r)
	}
	id := strconv.FormatInt(int64(r["id"].(float64)), 10)
	base := "/api/trade/" + id
	a.do("POST", "/api/live/accounts/"+id+"/arm", map[string]any{"trade_password": "trade-pw-8899"})
	a.until(base, func(v map[string]any) bool { return v["wallet"] == "10000" })

	// 从这里下的单：不提醒（手续费让钱包变了，也算得清）
	if st, r := a.do("POST", base+"/orders", map[string]any{"instrument": "BTC-PERP", "side": "buy", "type": "market", "qty": "0.1"}); st != 200 {
		t.Fatalf("下单：%d %v", st, r)
	}
	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	if len(got) != 0 {
		t.Fatalf("从这里下的单不该提醒：%v", got)
	}
	mu.Unlock()

	// 别处（币安 App）下的单
	e.fake.External("BTCUSDT", "SELL", 0.05)
	p := wait("external")
	if p["inst"] != "BTC-PERP" || p["side"] != "sell" || p["qty"] != "0.05" || p["px"] != "60000" || p["account"] != "Main" {
		t.Fatalf("外部成交：%v", p)
	}

	// 从现货钱包划转进来 500：用交易所的资金流水说明
	e.fake.Transfer(500)
	p = wait("money")
	if p["amount"] != "500.00" || p["transfer"] != "500.00" || p["rest"] != "" {
		t.Fatalf("划转：%v", p)
	}
	// 起算点改成交易所的数：之后不再提醒
	a.until(base, func(v map[string]any) bool { return v["initial"] == "10500" })

	// 密钥在币安那边被删了：连续失败一阵才提醒，恢复了再说一声
	e.fake.SetKey("revoked-0123456789abcdef")
	p = wait("fail")
	if !strings.Contains(p["err"], "-2015") || strings.Contains(p["err"], "signature") {
		t.Fatalf("对账失败：%v", p)
	}
	e.fake.SetKey(fKey)
	wait("ok")

	time.Sleep(1 * time.Second)
	for what, want := range map[string]int{"external": 1, "money": 1, "fail": 1, "ok": 1} {
		if n, _ := count(what); n != want {
			mu.Lock()
			t.Fatalf("%s 提醒了 %d 次：%v", what, n, got)
		}
	}
}

func TestLiqKind(t *testing.T) {
	for c, want := range map[string]string{"autoclose-1700000000": "liq", "adl_autoclose": "adl", "web_123": "", "qsm-abc": ""} {
		if got := liqKind(c); got != want {
			t.Fatalf("%s：%q", c, got)
		}
	}
}
