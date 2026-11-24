package notify

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 自定义提醒条件（18 第 10.4 条）：检查规则、从不满足变成满足才响、冷静期、只提醒一次、暂停、取不到的数算不满足、通知文字
func TestRules(t *testing.T) {
	s := New(nil)
	now := int64(1_800_000_000_000)
	s.Now = func() int64 { return now }
	ctx := context.Background()
	bad := []Rule{
		{Inst: "BTC-PERP"},
		{Inst: "BTC-PERP", Conds: []Cond{{Field: "rm -rf", Op: ">", Value: 1}}},
		{Inst: "BTC-PERP", Conds: []Cond{{Field: "px", Op: "=", Value: 1}}},
		{Inst: "BTC-PERP", Conds: []Cond{{Field: "px", Op: ">", Value: -1}}},
		{Inst: "BTC-PERP", Conds: []Cond{{Field: "px", Op: ">", Value: 1}, {Field: "px", Op: ">", Value: 1}, {Field: "px", Op: ">", Value: 1}, {Field: "px", Op: ">", Value: 1}, {Field: "px", Op: ">", Value: 1}}},
		{Inst: "BTC-PERP", Cooldown: 2, Conds: []Cond{{Field: "px", Op: ">", Value: 1}}},
		{Inst: "", Conds: []Cond{{Field: "px", Op: ">", Value: 1}}},
	}
	for i, r := range bad {
		if _, err := s.AddRule(ctx, 1, r); err == nil {
			t.Fatalf("第 %d 条不对的规则应该被拒：%+v", i, r)
		}
	}
	r, err := s.AddRule(ctx, 1, Rule{Name: "资金费过热", Inst: "BTC-PERP", Conds: []Cond{{Field: "funding", Op: ">", Value: 0.05}, {Field: "chg24h", Op: ">", Value: 5}}})
	if err != nil || r.Join != "all" || r.Cooldown != 60 {
		t.Fatalf("%v %+v", err, r)
	}
	m := map[string]float64{"funding": 0.06, "chg24h": 6, "px": 70000}
	metrics := func(inst string) map[string]float64 {
		if inst == "BTC-PERP" {
			return m
		}
		return nil
	}
	count := func() int {
		items, _, _ := s.List(ctx, 1, 50)
		return len(items)
	}
	// 加的时候已经满足：不响，等它先变成不满足
	s.CheckRules(ctx, metrics)
	if count() != 0 {
		t.Fatal("一加上就响")
	}
	m["chg24h"] = 4
	s.CheckRules(ctx, metrics)
	m["chg24h"] = 6
	s.CheckRules(ctx, metrics)
	if count() != 1 {
		t.Fatalf("从不满足变成满足要响一次：%d", count())
	}
	items, _, _ := s.List(ctx, 1, 50)
	if txt := Render("zh", items[0].Kind, items[0].Params); !strings.Contains(txt, "资金费过热") || !strings.Contains(txt, "资金费 0.0600%（高于 0.05%）") || !strings.Contains(txt, "24 小时涨跌 6.00%（高于 5%）") {
		t.Fatalf("通知文字：%s", txt)
	}
	// 一直满足：不再响
	s.CheckRules(ctx, metrics)
	// 掉下去再上来，但还在冷静期（1 小时）
	m["chg24h"] = 1
	s.CheckRules(ctx, metrics)
	now += 30 * 60_000
	m["chg24h"] = 7
	s.CheckRules(ctx, metrics)
	if count() != 1 {
		t.Fatal("冷静期里不再响")
	}
	m["chg24h"] = 1
	s.CheckRules(ctx, metrics)
	now += 31 * 60_000
	m["chg24h"] = 7
	s.CheckRules(ctx, metrics)
	if count() != 2 {
		t.Fatalf("过了冷静期，重新满足要响：%d", count())
	}
	// 取不到的数算不满足；"任一满足"
	r2, _ := s.AddRule(ctx, 1, Rule{Inst: "BTC-PERP", Join: "any", Once: true, Conds: []Cond{{Field: "oi1h", Op: ">", Value: 3}, {Field: "px", Op: "<", Value: 60000}}})
	s.CheckRules(ctx, metrics) // 这时都不满足（oi1h 取不到，价格 70000）
	m["px"] = 59000
	s.CheckRules(ctx, metrics)
	if count() != 3 {
		t.Fatalf("任一满足：%d", count())
	}
	for _, x := range s.Rules(1) {
		if x.ID == r2.ID && !x.Off {
			t.Fatal("只提醒一次的，响完就停")
		}
	}
	// 暂停的不响；别人的删不了
	s.SetRuleOff(ctx, 1, r.ID, true)
	m["chg24h"] = 1
	s.CheckRules(ctx, metrics)
	m["chg24h"] = 9
	now += 2 * 3600_000
	s.CheckRules(ctx, metrics)
	if count() != 3 {
		t.Fatal("暂停的不响")
	}
	if s.DelRule(ctx, 2, r.ID) || !s.DelRule(ctx, 1, r.ID) || len(s.Rules(1)) != 1 {
		t.Fatal("只能删自己的")
	}
	for in, want := range map[float64]string{3310.773813387048: "3310.77", 0.06123456: "0.0612346", 106000.4: "106000", 2.5: "2.5", -4.0: "-4"} {
		if got := sig6(in); got != want {
			t.Fatalf("sig6(%v) = %s，应该是 %s", in, got, want)
		}
	}
	if en := ruleText(true, "SOL", map[string]string{"conds": "rv>3=4.5;px<100=95.5"}); en != "Condition alert · SOL: volume multiple 4.5× (above 3×); price 95.5 (below 100)" {
		t.Fatalf("英文：%s", en)
	}
}

// 接上数据库：Attach 不能卡住（11-14 出过：Attach 锁着的时候 attachRules 又去锁，启动卡死、注册一直 503），规则存进去、重启读得回来
func TestRulesPersist(t *testing.T) {
	dsn := os.Getenv("QS_TEST_DB")
	if dsn == "" {
		t.Skip("没设 QS_TEST_DB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const user = 987654
	_, _ = pool.Exec(ctx, `delete from alert_rules where user_id = $1`, user)
	attach := func() *Service {
		s := New(nil)
		done := make(chan error, 1)
		go func() { done <- s.Attach(ctx, pool) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Attach 卡住了")
		}
		return s
	}
	s := attach()
	r, err := s.AddRule(ctx, user, Rule{Name: "x", Inst: "BTC-PERP", Conds: []Cond{{Field: "chg1h", Op: "<", Value: -3}}})
	if err != nil {
		t.Fatal(err)
	}
	s.SetRuleOff(ctx, user, r.ID, true)
	s2 := attach()
	got := s2.Rules(user)
	if len(got) != 1 || got[0].Name != "x" || !got[0].Off || got[0].Conds[0].Field != "chg1h" {
		t.Fatalf("重启读回来：%+v", got)
	}
	s2.DeleteUser(ctx, user)
	if n := len(attach().Rules(user)); n != 0 {
		t.Fatalf("删账户时一起删：%d", n)
	}
}

// D103 时修的：加规则时价格还没到线，第一次检查前价格就穿过去了——要响（原来一律当成"加的时候就满足"，这一次不响）。
// 加的时候已经满足的照样不响；恢复（暂停后打开）也按此刻判断。
func TestRuleStartsFromNow(t *testing.T) {
	s := New(nil)
	now := int64(1_800_000_000_000)
	s.Now = func() int64 { return now }
	ctx := context.Background()
	px := 3480.0
	metrics := func(inst string) map[string]float64 {
		if inst == "ETH-PERP" {
			return map[string]float64{"px": px}
		}
		return nil
	}
	s.RuleMetrics = metrics
	count := func() int {
		items, _, _ := s.List(ctx, 1, 50)
		return len(items)
	}
	r, err := s.AddRule(ctx, 1, Rule{Name: "ETH 回调", Inst: "ETH-PERP", Conds: []Cond{{Field: "px", Op: "<", Value: 3375}}})
	if err != nil {
		t.Fatal(err)
	}
	px = 3300 // 第一次检查之前就穿过去了
	s.CheckRules(ctx, metrics)
	if count() != 1 {
		t.Fatalf("加的时候没到线、之后穿过去：要响一次，响了 %d 次", count())
	}
	// 加的时候已经在线下面：不响
	met, err := s.AddRule(ctx, 1, Rule{Name: "已经满足", Inst: "ETH-PERP", Conds: []Cond{{Field: "px", Op: "<", Value: 3400}}})
	if err != nil {
		t.Fatal(err)
	}
	// 界面要能说"现在已满足，等下一次再响"，不能只写"还没响过"（11-17 走查）
	if met.Now == nil || !*met.Now || r.Now == nil || *r.Now {
		t.Fatalf("返回的 now 不对：已经满足的 %v，加的时候没满足的 %v", met.Now, r.Now)
	}
	s.CheckRules(ctx, metrics)
	if count() != 1 {
		t.Fatalf("加的时候已经满足的不该响：%d", count())
	}
	// 暂停时价格回到线上面，恢复后再跌下去：要响（冷静期过了以后）
	now += 2 * 3600_000
	s.SetRuleOff(ctx, 1, r.ID, true)
	px = 3480
	s.SetRuleOff(ctx, 1, r.ID, false)
	px = 3300
	s.CheckRules(ctx, metrics)
	if count() != 2 {
		t.Fatalf("恢复后从不满足变成满足要响：%d", count())
	}
}
