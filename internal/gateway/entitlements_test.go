package gateway

import (
	"encoding/base64"
	"encoding/json"
	"mirasim2api/internal/mirasim"
	"mirasim2api/internal/store"
	"net/http"
	"reflect"
	"testing"
)

func TestModelsForGoAreNotGenericFallback(t *testing.T) {
	fx := newFixture(t, okRelay, nil)
	e := fx.addAccount(t, "go")
	priv, _ := mirasim.GenerateDeviceKey()
	token := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"plan":"go"}`)) + ".s"
	e.Client, _ = mirasim.NewClient(token, priv, fx.relay.srv.URL, fx.relay.srv.URL, "", "", "")
	key := fx.createKey(t, "test", store.APIKeyOpts{})
	req, _ := http.NewRequest("GET", fx.gw.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Source string `json:"source"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range data.Data {
		ids = append(ids, m.ID)
	}
	if !reflect.DeepEqual(ids, []string{"kimi-k3", "deepseek-flash", "glm-5.3-flash"}) || data.Source != "plan:go" {
		t.Fatalf("wrong Go catalogue: %v", ids)
	}
}
