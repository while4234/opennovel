package modelprofile

import "strings"

// Role identifies a hidden runtime context budget. These values are internal
// policy keys rather than user-facing settings.
type Role string

const (
	RoleCoordinator Role = "coordinator"
	RoleArchitect   Role = "architect"
	RoleCharacter   Role = "character"
	RoleWriter      Role = "writer"
	RoleEditor      Role = "editor"
)

// Profile contains model-specific limits established by the context benchmark.
// Unknown models deliberately use the conservative default profile, so adding a
// provider never requires a web setting or a global-limit migration.
type Profile struct {
	Name                      string
	ContextWindows            map[Role]int
	FoundationMergeBatchRunes int
}

var defaultProfile = Profile{
	Name: "default",
	ContextWindows: map[Role]int{
		RoleCoordinator: 64_000,
		RoleArchitect:   96_000,
		RoleCharacter:   96_000,
		RoleWriter:      96_000,
		RoleEditor:      128_000,
	},
	FoundationMergeBatchRunes: 70_000,
}

var knownProfiles = []struct {
	aliases []string
	profile Profile
}{
	{
		aliases: []string{"deepseek-v4-pro", "deepseek v4 pro"},
		profile: Profile{
			Name: "deepseek-v4-pro",
			ContextWindows: map[Role]int{
				RoleCoordinator: 64_000,
				RoleArchitect:   96_000,
				RoleCharacter:   96_000,
				RoleWriter:      128_000,
				RoleEditor:      128_000,
			},
			FoundationMergeBatchRunes: 40_000,
		},
	},
	{
		aliases: []string{"grok-4.5", "grok 4.5"},
		profile: Profile{
			Name: "grok-4.5",
			ContextWindows: map[Role]int{
				RoleCoordinator: 64_000,
				RoleArchitect:   96_000,
				RoleCharacter:   64_000,
				RoleWriter:      64_000,
				RoleEditor:      64_000,
			},
			FoundationMergeBatchRunes: 60_000,
		},
	},
}

// Resolve returns a copy so callers cannot mutate the process-wide registry.
func Resolve(modelName string) Profile {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	for _, candidate := range knownProfiles {
		for _, alias := range candidate.aliases {
			if strings.Contains(normalized, alias) {
				return clone(candidate.profile)
			}
		}
	}
	return clone(defaultProfile)
}

func (p Profile) ContextWindow(role Role) int {
	return p.ContextWindows[role]
}

func clone(profile Profile) Profile {
	copyProfile := profile
	copyProfile.ContextWindows = make(map[Role]int, len(profile.ContextWindows))
	for role, window := range profile.ContextWindows {
		copyProfile.ContextWindows[role] = window
	}
	return copyProfile
}
