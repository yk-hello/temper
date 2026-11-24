package trading

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestADLEstimate(t *testing.T) {
	d := decimal.NewFromFloat
	for _, c := range []struct {
		roe, lev float64
		want     int
	}{{-0.1, 10, 0}, {0, 10, 0}, {0.01, 10, 1}, {0.03, 10, 2}, {0.1, 10, 3}, {0.2, 10, 4}, {0.2, 2, 2}} {
		if got := ADLEstimate(d(c.roe), d(c.lev)); got != c.want {
			t.Fatalf("收益率 %v、杠杆 %v：%d，应该是 %d", c.roe, c.lev, got, c.want)
		}
	}
}
