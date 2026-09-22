package upstream

import "testing"

// TestParseBadge 上游 badge 形如 "badge:<文案>:<#颜色>"，颜色必须剥离。
func TestParseBadge(t *testing.T) {
	cases := []struct {
		tags      []string
		wantLabel string
		wantColor string
	}{
		{[]string{"badge:独家优惠:#FF0000"}, "独家优惠", "#FF0000"},
		{[]string{"badge:夜间折扣:#1E90FF"}, "夜间折扣", "#1E90FF"},
		{[]string{"badge:限时免费"}, "限时免费", ""}, // 无颜色后缀
		{[]string{"craft", "badge:Free now:#00FF00"}, "Free now", "#00FF00"},
		{[]string{"badge:文案:#FFF"}, "文案", "#FFF"}, // 3 位色
		{[]string{"no-badge-here"}, "", ""},
		{nil, "", ""},
		// 冒号但后缀不是颜色 → 应整体当作文案
		{[]string{"badge:限时:免费"}, "限时:免费", ""},
	}
	for _, c := range cases {
		gotLabel, gotColor := parseBadge(c.tags)
		if gotLabel != c.wantLabel || gotColor != c.wantColor {
			t.Errorf("parseBadge(%q) = (%q, %q), want (%q, %q)",
				c.tags, gotLabel, gotColor, c.wantLabel, c.wantColor)
		}
	}
}
