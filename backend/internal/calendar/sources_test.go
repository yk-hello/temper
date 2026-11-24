package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 内置的劳工统计局日程补到了 2027 年底：最后一条是 2027-12-15 的 PPI（美东 08:30）
func TestBuiltinUntil2027(t *testing.T) {
	want, _ := at("2027-12-15", 8, 30)
	if got := BuiltinUntil(); got != want {
		t.Fatalf("内置日程到 %v，应该到 %v", time.UnixMilli(got).In(eastern), time.UnixMilli(want).In(eastern))
	}
	n := map[string]int{}
	for _, e := range builtinBLS() {
		if time.UnixMilli(e.T).In(eastern).Year() == 2027 {
			n[e.Key]++
		}
	}
	if n["cpi"] != 12 || n["nfp"] != 12 || n["ppi"] != 12 {
		t.Fatalf("2027 年每种 12 次：%v", n)
	}
}

// 同一类同一天只留一条，前面的来源优先（FRED 只有日期，时间不准，放最后）
func TestMergePrefersPrecise(t *testing.T) {
	precise, _ := at("2027-01-13", 8, 30)
	rough, _ := at("2027-01-13", 9, 0)
	other, _ := at("2027-02-11", 8, 30)
	got := merge([]Event{{T: precise, Key: "cpi"}}, []Event{{T: rough, Key: "cpi"}, {T: other, Key: "cpi"}, {T: other, Key: ""}})
	if len(got) != 2 || got[0].T != precise || got[1].T != other {
		t.Fatalf("%+v", got)
	}
}

// FRED：按名字挑出要的几种、议息 14:00、翻页、密钥不对说清楚、错误里不带密钥
func TestFetchFRED(t *testing.T) {
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "goodkey" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error_code":400,"error_message":"Bad Request.  The value for variable api_key is not registered."}`))
			return
		}
		pages++
		var rows []string
		if r.URL.Query().Get("offset") == "0" {
			rows = append(rows, `{"release_id":10,"release_name":"Consumer Price Index","date":"2028-01-12"}`,
				`{"release_id":101,"release_name":"FOMC Press Release","date":"2028-01-26"}`,
				`{"release_id":999,"release_name":"H.4.1 Factors Affecting Reserve Balances","date":"2028-01-27"}`)
			for i := len(rows); i < 1000; i++ {
				rows = append(rows, `{"release_id":1,"release_name":"Something Else","date":"2028-01-01"}`)
			}
		} else {
			rows = append(rows, `{"release_id":50,"release_name":"Employment Situation","date":"2028-01-07"}`)
		}
		_, _ = fmt.Fprintf(w, `{"release_dates":[%s]}`, strings.Join(rows, ","))
	}))
	defer srv.Close()
	s := &Service{FREDKey: "goodkey", FREDBase: srv.URL}
	got, err := s.fetchFRED(context.Background())
	if err != nil || len(got) != 3 || pages != 2 {
		t.Fatalf("%v %+v（%d 页）", err, got, pages)
	}
	keys := map[string]int64{}
	for _, e := range got {
		keys[e.Key] = e.T
	}
	if f, _ := at("2028-01-26", 14, 0); keys["fomc"] != f {
		t.Fatalf("议息按美东 14:00：%v", time.UnixMilli(keys["fomc"]).In(eastern))
	}
	if c, _ := at("2028-01-12", 8, 30); keys["cpi"] != c || keys["nfp"] == 0 {
		t.Fatalf("CPI、非农：%v", keys)
	}
	s.FREDKey = "badkey"
	if _, err := s.fetchFRED(context.Background()); err == nil || !strings.Contains(err.Error(), "FRED_API_KEY") || strings.Contains(err.Error(), "badkey") {
		t.Fatalf("密钥不对：%v", err)
	}
	s.FREDKey = ""
	if got, err := s.fetchFRED(context.Background()); err != nil || got != nil {
		t.Fatal("没配密钥就不取")
	}
}
