package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

type skillStatusHost struct {
	Host
	state  SkillState
	detail SkillDetail
	prompt string
}

func (h *skillStatusHost) Skills() (SkillState, error) {
	return h.state, nil
}

func (h *skillStatusHost) SkillContent(name string) (SkillDetail, error) {
	if name != h.detail.Name {
		return SkillDetail{}, errors.New("not found")
	}
	return h.detail, nil
}

func (h *skillStatusHost) LoadSkillPrompt(name, args string) (string, error) {
	if name != h.detail.Name {
		return "", errors.New("not found")
	}
	if args != "" {
		return h.prompt + "\n\n" + args, nil
	}
	return h.prompt, nil
}

func (h *skillStatusHost) SetSkillEnabled(_ context.Context, name string, enabled bool) (SkillState, error) {
	if name != h.state.Skills[0].Name {
		return SkillState{}, errors.New("not found")
	}
	h.state.Skills[0].Disabled = !enabled
	return h.state, nil
}

func TestSkillSwitchRequiresAnExplicitBoolean(t *testing.T) {
	host := &skillStatusHost{state: SkillState{CanToggle: true, Skills: []SkillInfo{{Name: "release"}}}}
	server := projectServer(t, host, nil)
	for _, input := range []string{`{"name":"release"}`, `{"name":"release","enabled":null}`, `{"enabled":false}`} {
		resp := postJSON(t, server.URL+"/api/skills/enabled", input)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("switch with %s returned %d, want 400", input, resp.StatusCode)
		}
	}
	resp := postJSON(t, server.URL+"/api/skills/enabled", `{"name":"release","enabled":false}`)
	defer resp.Body.Close()
	var state SkillState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil || resp.StatusCode != http.StatusOK || !state.CanToggle || len(state.Skills) != 1 || !state.Skills[0].Disabled || state.Roots == nil {
		t.Fatalf("disable state = %+v, status = %d, error = %v", state, resp.StatusCode, err)
	}
}

func TestSkillRoutesListReadAndLoad(t *testing.T) {
	plain := projectServer(t, &projectHost{}, nil)
	resp, err := http.Get(plain.URL + "/api/skills")
	if err != nil {
		t.Fatalf("get without SkillStatus: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("without SkillStatus status = %d, want 501", resp.StatusCode)
	}

	host := &skillStatusHost{
		state: SkillState{
			Cwd:   "/work/app",
			Roots: []SkillRootInfo{{Scope: "user", Dir: "/home/.atlas/skills"}},
			Skills: []SkillInfo{
				{Name: "release", Description: "Cut a release.", Path: "/home/.atlas/skills/release/SKILL.md", Scope: "user"},
			},
		},
		detail: SkillDetail{
			SkillInfo: SkillInfo{Name: "release", Description: "Cut a release.", Path: "/home/.atlas/skills/release/SKILL.md", Scope: "user"},
			Body:      "---\nname: release\n---\nSteps here.\n",
		},
		prompt: "<skill name=\"release\" location=\"/home/.atlas/skills/release/SKILL.md\">\nSteps here.\n</skill>",
	}
	server := projectServer(t, host, nil)

	listResp, err := http.Get(server.URL + "/api/skills")
	if err != nil {
		t.Fatalf("GET /api/skills: %v", err)
	}
	defer listResp.Body.Close()
	var gotState SkillState
	if err := json.NewDecoder(listResp.Body).Decode(&gotState); err != nil {
		t.Fatalf("decode SkillState: %v", err)
	}
	if len(gotState.Skills) != 1 || gotState.Skills[0].Name != "release" {
		t.Fatalf("gotState = %+v", gotState)
	}

	contentResp, err := http.Get(server.URL + "/api/skills/content?name=release")
	if err != nil {
		t.Fatalf("GET /api/skills/content: %v", err)
	}
	defer contentResp.Body.Close()
	var gotDetail SkillDetail
	if err := json.NewDecoder(contentResp.Body).Decode(&gotDetail); err != nil {
		t.Fatalf("decode SkillDetail: %v", err)
	}
	if gotDetail.Body == "" || gotDetail.Name != "release" {
		t.Fatalf("gotDetail = %+v", gotDetail)
	}

	loadResp := postJSON(t, server.URL+"/api/skills/load", `{"name":"release","args":"v1.0"}`)
	defer loadResp.Body.Close()
	if loadResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/skills/load status = %d, want 200", loadResp.StatusCode)
	}
	var loaded struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(loadResp.Body).Decode(&loaded); err != nil {
		t.Fatalf("decode loaded prompt: %v", err)
	}
	if loaded.Prompt == "" {
		t.Fatal("empty loaded prompt")
	}
}
