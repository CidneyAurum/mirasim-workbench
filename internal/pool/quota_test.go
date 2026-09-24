package pool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mirasim2api/internal/mirasim"
	"mirasim2api/internal/store"
	"reflect"
	"testing"
	"time"
)

func TestQuotaWindowMetadata(t *testing.T) {
	var w Window
	err := json.Unmarshal([]byte(`{"name":"7d","used":150.5739624,"budget":5000,"reset_at":"2026-10-01T12:00:00Z","model_scoped":true}`), &w)
	if err != nil || w.Name != "7d" || w.Used != 150.5739624 || w.Budget != 5000 || !w.ModelScoped || string(w.ResetAt) != `"2026-10-01T12:00:00Z"` {
		t.Fatalf("quota metadata lost: %+v, %v", w, err)
	}
	for _, raw := range []string{`{"name":"7d"}`, `{"used":null,"budget":500}`, `{"used":1,"budget":null}`} {
		if json.Unmarshal([]byte(raw), &w) == nil {
			t.Fatal("missing quota parsed as zero")
		}
	}
}

func TestGoPlanCatalogue(t *testing.T) {
	want := []string{"kimi-k3", "deepseek-flash", "glm-5.3-flash"}
	if !reflect.DeepEqual(ModelsForPlan("go"), want) {
		t.Fatal("wrong Go models")
	}
	p := newTestPool(t)
	p.Add(&Entry{Account: &store.Account{ID: "go", Enabled: true, RefreshToken: "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"plan":"go"}`)) + ".s"}})
	if !reflect.DeepEqual(p.ModelAllowlist(), want) {
		t.Fatal("Go pool must not expose generic models")
	}
	p.Add(&Entry{Account: &store.Account{ID: "unknown", Enabled: true, RefreshToken: "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"plan":"unknown"}`)) + ".s"}})
	if p.ModelAllowlist() != nil {
		t.Fatal("do not guess restrictions for mixed/unknown plans")
	}
}

func TestQuotaRefreshPopulatesMetadataImmediately(t *testing.T) {
	p := newTestPool(t)
	e := newEntry("quota", &mockFetcher{limits: &Limits{Windows: []Window{{Name: "7d", Used: 30, Budget: 100}}}})
	priv, _ := mirasim.GenerateDeviceKey()
	e.Client, _ = mirasim.NewClient("fixture", priv, "", "", "", "", "")
	p.Add(e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.StartLimitsLoop(ctx)
	deadline := time.Now().Add(time.Second)
	for e.LimitsSnapshot() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.LimitsSnapshot() == nil {
		t.Fatal("quota loop must refresh on startup, not after ten minutes")
	}
	stats := p.Stats()
	if len(stats[0].LimitsWindows) != 1 || stats[0].LimitsWindows[0].Name != "7d" || stats[0].LimitsFetchedAt.IsZero() {
		t.Fatal("Stats discarded quota metadata")
	}
}
