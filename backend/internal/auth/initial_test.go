package auth

import "testing"

// D103：模拟资金不限额度，大于 0 就行（最多 8 位小数），带逗号也认。
func TestParseInitial(t *testing.T) {
	for in, want := range map[string]string{"10000": "10000", "2,500": "2500", " 100 ": "100", "0.5": "0.5", "1000000000000": "1000000000000", "5000.50": "5000.5", "1.123456789": "1.12345679"} {
		if got, err := ParseInitial(in); err != nil || got != want {
			t.Fatalf("%q → %q %v，应为 %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-1000", "0.000000001", "abc", "1e3x"} {
		if _, err := ParseInitial(in); err == nil {
			t.Fatalf("%q 应该被拒", in)
		}
	}
}
