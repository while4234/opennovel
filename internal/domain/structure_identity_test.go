package domain

import (
	"strings"
	"testing"
)

func TestLegacyStructureIDIsDeterministicAndProjectScoped(t *testing.T) {
	first := LegacyStructureID("project-a", StructureKindChapter, `chapters\0007`)
	second := LegacyStructureID("project-a", StructureKindChapter, "chapters/0007")
	if first != second {
		t.Fatalf("legacy ID changed for the same normalized path: %q != %q", first, second)
	}
	if first == LegacyStructureID("project-b", StructureKindChapter, "chapters/0007") {
		t.Fatal("legacy ID must be scoped by project identity")
	}
	if first == LegacyStructureID("project-a", StructureKindChapter, "chapters/0008") {
		t.Fatal("legacy ID must include the original structural path")
	}
}

func TestNewStructureIDIsUniqueAndKindPrefixed(t *testing.T) {
	first, err := NewStructureID(StructureKindArc)
	if err != nil {
		t.Fatalf("first ID: %v", err)
	}
	second, err := NewStructureID(StructureKindArc)
	if err != nil {
		t.Fatalf("second ID: %v", err)
	}
	if first == second {
		t.Fatalf("new structure IDs collided: %q", first)
	}
	if !strings.HasPrefix(first, "arc_") || !strings.HasPrefix(second, "arc_") {
		t.Fatalf("unexpected arc ID prefixes: %q %q", first, second)
	}
}

func TestProjectLayeredOutlineOrderPreservesIDs(t *testing.T) {
	volumes := []VolumeOutline{
		{ID: "vol-b", Index: 9, Arcs: []ArcOutline{{ID: "arc-b", Index: 8, Chapters: []OutlineEntry{{ID: "ch-b", Chapter: 90}}}}},
		{ID: "vol-a", Index: 7, Arcs: []ArcOutline{{ID: "arc-a", Index: 6, Chapters: []OutlineEntry{{ID: "ch-a", Chapter: 70}}}}},
	}
	projected := ProjectLayeredOutlineOrder(volumes)
	if projected[0].Index != 1 || projected[1].Index != 2 {
		t.Fatalf("volume order not projected: %+v", projected)
	}
	if projected[0].Arcs[0].Index != 1 || projected[1].Arcs[0].Index != 1 {
		t.Fatalf("arc order not projected: %+v", projected)
	}
	if projected[0].Arcs[0].Chapters[0].Chapter != 1 || projected[1].Arcs[0].Chapters[0].Chapter != 2 {
		t.Fatalf("chapter order not projected: %+v", projected)
	}
	if projected[0].ID != "vol-b" || projected[0].Arcs[0].ID != "arc-b" || projected[0].Arcs[0].Chapters[0].ID != "ch-b" {
		t.Fatalf("projection changed stable IDs: %+v", projected[0])
	}
}
