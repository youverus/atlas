package webhost

import (
	"context"

	"pi-go/internal/assembly"
	"pi-go/internal/httpapi"
)

func (h *Host) skillsCwd() string {
	if cwd := h.SessionDirectory(); cwd != "" {
		return cwd
	}
	return h.root()
}

// Skills scans installed skills visible to the currently viewed session's
// working directory without disturbing any running session.
func (h *Host) Skills() (httpapi.SkillState, error) {
	return skillState(h.skillsCwd())
}

func skillState(cwd string) (httpapi.SkillState, error) {
	snapshot, err := assembly.DescribeSkills(cwd)
	if err != nil {
		return httpapi.SkillState{}, err
	}
	roots := make([]httpapi.SkillRootInfo, 0, len(snapshot.Roots))
	for _, root := range snapshot.Roots {
		roots = append(roots, httpapi.SkillRootInfo{
			Scope: root.Scope,
			Dir:   root.Dir,
		})
	}
	skills := make([]httpapi.SkillInfo, 0, len(snapshot.Skills))
	for _, s := range snapshot.Skills {
		skills = append(skills, httpapi.SkillInfo{
			Name:        s.Name,
			Description: s.Description,
			Path:        s.Path,
			Scope:       s.Scope,
			Disabled:    s.Disabled,
		})
	}
	return httpapi.SkillState{
		Cwd:       snapshot.Cwd,
		Roots:     roots,
		Skills:    skills,
		Notes:     append([]string(nil), snapshot.Notes...),
		CanToggle: true,
	}, nil
}

// SetSkillEnabled saves the user's switch for one installed skill.
func (h *Host) SetSkillEnabled(_ context.Context, name string, enabled bool) (httpapi.SkillState, error) {
	cwd := h.skillsCwd()
	if err := assembly.SetSkillEnabled(cwd, name, enabled); err != nil {
		return httpapi.SkillState{}, err
	}
	return skillState(cwd)
}

// SkillContent reads one skill's metadata and SKILL.md text for preview.
func (h *Host) SkillContent(name string) (httpapi.SkillDetail, error) {
	info, body, err := assembly.ReadSkillContent(h.skillsCwd(), name)
	if err != nil {
		return httpapi.SkillDetail{}, err
	}
	return httpapi.SkillDetail{
		SkillInfo: httpapi.SkillInfo{
			Name:        info.Name,
			Description: info.Description,
			Path:        info.Path,
			Scope:       info.Scope,
		},
		Body: body,
	}, nil
}

// LoadSkillPrompt renders the formatted <skill ...> prompt for starting a turn.
func (h *Host) LoadSkillPrompt(name, args string) (string, error) {
	return assembly.BuildSkillPrompt(h.skillsCwd(), name, args)
}
