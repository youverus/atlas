package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

type modelConfigHost struct {
	Host
	cfg     ModelConfig
	saved   *ModelConfig
	saveErr error
}

func (h *modelConfigHost) ModelConfig() (ModelConfig, error) {
	return h.cfg, nil
}

func (h *modelConfigHost) SaveModelConfig(next ModelConfig) error {
	if h.saveErr != nil {
		return h.saveErr
	}
	h.saved = &next
	h.cfg = next
	return nil
}

func TestModelConfigRoutesReturn501WithoutEditorAndRoundTripWithEditor(t *testing.T) {
	plain := projectServer(t, &projectHost{}, nil)
	resp, err := http.Get(plain.URL + "/api/config/models")
	if err != nil {
		t.Fatalf("get without editor: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("GET status without editor = %d, want 501", resp.StatusCode)
	}

	host := &modelConfigHost{
		Host: &projectHost{},
		cfg: ModelConfig{
			ConfigPath:   "/tmp/atlas/config.json",
			DefaultModel: "p1/m1",
			Providers: []ProviderConfig{
				{
					Name:         "p1",
					API:          "openai-completions",
					APIKeyEnv:    "DEEPSEEK_API_KEY",
					APIKeyEnvSet: true,
					Models:       []ModelItemConfig{{ID: "m1", Name: "Model One", ContextWindow: 128000}},
				},
			},
		},
	}
	server := projectServer(t, host, nil)

	getResp, err := http.Get(server.URL + "/api/config/models")
	if err != nil {
		t.Fatalf("get /api/config/models: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getResp.StatusCode)
	}
	var got ModelConfig
	if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode model config: %v", err)
	}
	if got.ConfigPath != "/tmp/atlas/config.json" || len(got.Providers) != 1 || got.Providers[0].Name != "p1" || !got.Providers[0].APIKeyEnvSet {
		t.Fatalf("unexpected model config: %+v", got)
	}

	payload, _ := json.Marshal(ModelConfig{
		DefaultModel: "p2/m2",
		Providers: []ProviderConfig{
			{
				Name:   "p2",
				API:    "anthropic-messages",
				APIKey: "k2",
				Models: []ModelItemConfig{{ID: "m2", Name: "Claude", Reasoning: true}},
			},
		},
	})
	postResp := postJSON(t, server.URL+"/api/config/models", string(payload))
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d, want 200", postResp.StatusCode)
	}
	if host.saved == nil || host.saved.DefaultModel != "p2/m2" || len(host.saved.Providers) != 1 {
		t.Fatalf("saved config mismatch: %+v", host.saved)
	}

	host.saveErr = errors.New("bad config")
	failResp := postJSON(t, server.URL+"/api/config/models", string(payload))
	failResp.Body.Close()
	if failResp.StatusCode != http.StatusConflict {
		t.Fatalf("failing save status = %d, want 409", failResp.StatusCode)
	}
}
