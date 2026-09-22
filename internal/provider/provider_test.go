package provider

import (
	"testing"
	"time"
)

// timeNow 返回 UTC+8 墙钟当前时刻（与 expireLoc 口径一致，测试用）。
func timeNow() time.Time { return time.Now().In(expireLoc) }

func TestExpiringWithin(t *testing.T) {
	today := timeNow().Format("2006-01-02")
	tomorrow := timeNow().AddDate(0, 0, 1).Format("2006-01-02")
	after := timeNow().AddDate(0, 0, 3).Format("2006-01-02")

	items := []ResourceItem{
		{Name: "今日到期", Remain: 100, Usable: true, ExpireAt: today},
		{Name: "明天到期", Remain: 50, Usable: true, ExpireAt: tomorrow},
		{Name: "三天后到期", Remain: 999, Usable: true, ExpireAt: after},
		{Name: "无到期时间", Remain: 70, Usable: true},
		{Name: "不可用池临期", Remain: 300, Usable: false, ExpireAt: today},
	}
	if got := ExpiringWithin(items, 24*time.Hour); got != 150 {
		t.Fatalf("expiring=%d want 150 (今天100+明天50，三天后/无到期/不可用不计)", got)
	}
}

func TestSummarizeExpiring(t *testing.T) {
	items := []ResourceItem{
		{Remain: 100, Usable: true, ExpireAt: timeNow().Format("2006-01-02")},
		{Remain: 200, Usable: false},
	}
	u, un := Summarize(items)
	e := ExpiringWithin(items, 24 * time.Hour)
	if u != 100 || un != 200 || e != 100 {
		t.Fatalf("usable=%d unusable=%d expiring=%d want 100/200/100", u, un, e)
	}
}
