package pool

import (
	"mirasim2api/internal/mirasim"
	"strings"
)

// Go's entitlement is not the repository's cross-plan fallback catalogue.
// Unknown / mixed plans are left to the upstream rather than guessed here.
func ModelsForPlan(plan string) []string {
	if strings.EqualFold(strings.TrimSpace(plan), "go") {
		return []string{"kimi-k3", "deepseek-flash", "glm-5.3-flash"}
	}
	return nil
}

func (p *Pool) ModelAllowlist() []string {
	known := false
	for _, e := range p.Entries() {
		if !e.Account.Enabled {
			continue
		}
		token := e.Account.RefreshToken
		if e.Client != nil {
			token = e.Client.RefreshToken()
		}
		plan, _ := mirasim.JWTClaims(token)["plan"].(string)
		if ModelsForPlan(plan) == nil {
			return nil
		}
		known = true
	}
	if known {
		return ModelsForPlan("go")
	}
	return nil
}
