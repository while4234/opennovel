package modelprofile

import "testing"

func TestResolveKnownProfiles(t *testing.T) {
	tests := []struct {
		model      string
		name       string
		character  int
		editor     int
		mergeRunes int
	}{
		{model: "deepseek-v4-pro", name: "deepseek-v4-pro", character: 96_000, editor: 128_000, mergeRunes: 40_000},
		{model: "GROK-4.5", name: "grok-4.5", character: 64_000, editor: 64_000, mergeRunes: 60_000},
		{model: "future-model", name: "default", character: 96_000, editor: 128_000, mergeRunes: 70_000},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			profile := Resolve(tt.model)
			if profile.Name != tt.name {
				t.Fatalf("name=%q, want %q", profile.Name, tt.name)
			}
			if got := profile.ContextWindow(RoleEditor); got != tt.editor {
				t.Fatalf("editor=%d, want %d", got, tt.editor)
			}
			if got := profile.ContextWindow(RoleCharacter); got != tt.character {
				t.Fatalf("character=%d, want %d", got, tt.character)
			}
			if profile.FoundationMergeBatchRunes != tt.mergeRunes {
				t.Fatalf("merge runes=%d, want %d", profile.FoundationMergeBatchRunes, tt.mergeRunes)
			}
		})
	}
}

func TestResolveReturnsIndependentProfile(t *testing.T) {
	profile := Resolve("grok-4.5")
	profile.ContextWindows[RoleWriter] = 1
	if got := Resolve("grok-4.5").ContextWindow(RoleWriter); got != 64_000 {
		t.Fatalf("registry mutated: writer=%d", got)
	}
}
