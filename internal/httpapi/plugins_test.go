package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

type pluginStatusHost struct {
	Host
	state PluginState
}

func (h *pluginStatusHost) Plugins() (PluginState, error) {
	return h.state, nil
}

func TestPluginsRouteListsInstalledPlugins(t *testing.T) {
	plain := projectServer(t, &projectHost{}, nil)
	resp, err := http.Get(plain.URL + "/api/plugins")
	if err != nil {
		t.Fatalf("get without PluginStatus: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}

	host := &pluginStatusHost{
		Host: &projectHost{},
		state: PluginState{
			Dir: "/home/.atlas/plugins",
			Plugins: []PluginInfo{
				{
					Name:        "review-pack",
					Description: "Code review skills and server",
					Dir:         "/home/.atlas/plugins/review-pack",
					SkillNames:  []string{"review"},
					Servers:     []string{"github"},
				},
			},
		},
	}
	srv := projectServer(t, host, nil)
	r, err := http.Get(srv.URL + "/api/plugins")
	if err != nil {
		t.Fatalf("get /api/plugins: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var got PluginState
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Plugins) != 1 || got.Plugins[0].Name != "review-pack" || len(got.Plugins[0].SkillNames) != 1 {
		t.Fatalf("unexpected plugin state: %+v", got)
	}
}
