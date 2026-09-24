package admin

import (
	"mirasim2api/internal/pool"
	"testing"
	"time"
)

func TestAccountViewIncludesQuotaAndGoModels(t *testing.T) {
	v := statToView(&pool.AccountStat{Plan: "go", LimitsFetched: true, LimitsWindows: []pool.Window{{Name: "7d", Used: 25, Budget: 500}}, LimitsFetchedAt: time.Now()})
	if len(v.LimitsWindows) != 1 || v.LimitsWindows[0].Name != "7d" || v.LimitsFetchedAt == 0 || v.LimitsStale || len(v.AllowedModels) != 3 {
		t.Fatalf("incomplete account quota view: %+v", v)
	}
	unknown := statToView(&pool.AccountStat{})
	if unknown.LimitsFetched || len(unknown.LimitsWindows) != 0 {
		t.Fatal("unknown quota must not become a fake zero window")
	}
}
