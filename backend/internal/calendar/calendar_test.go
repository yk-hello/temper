package calendar

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const sample = "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY:Personal Income and Outlays\\, November 2027\r\nDTSTART:20271222T133000Z\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nSUMMARY:U.S. International Trade in Goods and Services\\, November 2024\r\nDTSTART;VALUE=DATE-TIME:20250107T133000Z\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nSUMMARY:GDP (Advance Estimate)\\, 3rd Quarter 2026\r\nDTSTART;VALUE=DATE-TIME:20261029T123000Z\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nSUMMARY:Gross Domestic Product by State\r\nDTSTART:20261029T123000Z\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nSUMMARY:GDP (Third Estimate)\r\nDTSTART:soon\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func TestParseICS(t *testing.T) {
	got := ParseICS(strings.NewReader(sample))
	if len(got) != 2 || got[0].Key != "gdp" || got[1].Key != "pce" || got[0].T >= got[1].T {
		t.Fatalf("%+v", got)
	}
	if got[1].Title != "Personal Income and Outlays, November 2027" {
		t.Fatal(got[1].Title)
	}
	if got[0].T != time.Date(2026, 10, 29, 12, 30, 0, 0, time.UTC).UnixMilli() {
		t.Fatal(got[0].T)
	}
}

func TestBLS(t *testing.T) {
	got := ParseICS(strings.NewReader("BEGIN:VEVENT\r\nDTSTART;TZID=US-Eastern:20260115T083000\r\nSUMMARY:Consumer Price Index\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;TZID=US-Eastern:20260710T083000\r\nSUMMARY:Employment Situation\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;TZID=US-Eastern:20260320T100000\r\nSUMMARY:Employment Situation of Veterans\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;TZID=US-Eastern:20260115T083000\r\nSUMMARY:Real Earnings\r\nEND:VEVENT\r\n"))
	if len(got) != 2 || got[0].Key != "cpi" || got[1].Key != "nfp" {
		t.Fatalf("%+v", got)
	}
	// 冬天美东是 UTC-5，夏天是 UTC-4
	if got[0].T != time.Date(2026, 1, 15, 13, 30, 0, 0, time.UTC).UnixMilli() {
		t.Fatal(time.UnixMilli(got[0].T).UTC())
	}
	if got[1].T != time.Date(2026, 7, 10, 12, 30, 0, 0, time.UTC).UnixMilli() {
		t.Fatal(time.UnixMilli(got[1].T).UTC())
	}
}

func TestEmbeddedBLS(t *testing.T) {
	n := map[string]int{}
	for _, e := range ParseICS(bytes.NewReader(blsICS)) {
		n[e.Key]++
	}
	if n["cpi"] < 12 || n["nfp"] < 12 || n["ppi"] < 12 {
		t.Fatalf("官方日历文件读出来太少：%+v", n)
	}
}

func TestFomcAll(t *testing.T) {
	if n := len(fomcEvents()); n != len(fomc) {
		t.Fatalf("%d / %d", n, len(fomc))
	}
}
