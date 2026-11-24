package latency

import (
	"testing"
	"time"
)

func fixed(vs ...float64) func() float64 {
	i := 0
	return func() float64 { v := vs[i%len(vs)]; i++; return v }
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func TestHop(t *testing.T) {
	// 没测到（模拟行情）：跟随实测 = 立即
	m := &Model{Meter: &Meter{}, Rand: fixed(1, 0.25, 0.5)} // 抖动取 0（cos(π/2)=0），不抖
	if e, r := m.Hop(1); e != 0 || r != 0 {
		t.Fatalf("测不到时：%v %v", e, r)
	}
	// 实测 150 毫秒：一个往返之后按本地盘口成交，回报同时到
	m.Meter.add(150 * time.Millisecond)
	if e, r := m.Hop(1); ms(e) != 150 || ms(r) != 150 {
		t.Fatalf("实测：%v %v", e, r)
	}
	// 假装在法兰克福：那边的半个往返 + 本地行情晚到的半个往返之后成交；回报在那边的一个往返之后
	m.Mode = func(int64) string { return "frankfurt" }
	e, r := m.Hop(1)
	if ms(e) != 230.0/2+75 || ms(r) != 230 {
		t.Fatalf("法兰克福：%v %v", e, r)
	}
	// 东京比本地还近：不能早于本地看到的盘口成交
	m.Mode = func(int64) string { return "tokyo" }
	e, r = m.Hop(1)
	if ms(e) != 6.0/2+75 || r != e {
		t.Fatalf("东京：%v %v", e, r)
	}
	// 关闭
	m.Mode = func(int64) string { return "off" }
	if e, r := m.Hop(1); e != 0 || r != 0 {
		t.Fatal("关闭")
	}
	// 偶尔网络抖一下：3–8 倍
	m.Mode = func(int64) string { return "measured" }
	m.Rand = fixed(1, 0.25, 0.001, 0.5)
	if e, _ := m.Hop(1); ms(e) != 150*5.5 {
		t.Fatalf("抖一下：%v", e)
	}
}

func TestValidAndMeasured(t *testing.T) {
	if !Valid("measured") || !Valid("off") || !Valid("hongkong") || Valid("mars") {
		t.Fatal("模式")
	}
	m := &Meter{}
	for _, v := range []int{100, 102, 98, 101, 140, 99, 103, 97, 100, 180} {
		m.add(time.Duration(v) * time.Millisecond)
	}
	rtt, j, ok := m.Measured()
	if !ok || ms(rtt) != 101 || ms(j) != 79 {
		t.Fatalf("中位数 %v，抖动 %v", rtt, j)
	}
}
