package tools

import (
	"strings"
	"testing"
)

func TestDecodeOutlineEntriesAcceptsStructuredScenesWithoutDataLoss(t *testing.T) {
	entries, err := decodeOutlineEntries("expand_arc chapters", `{
		"chapters": [{
			"chapter": 1,
			"title": "董事会暗门",
			"core_event": "林舒然必须在董事会投票前确认内鬼。",
			"hook": "沈辞带回一份被改写的门禁记录。",
			"scenes": [
				"林舒然在车内复盘昨夜的监控。",
				{
					"location": "董事会休息室",
					"characters": ["林舒然", "沈辞"],
					"goal": "确认谁接触过原始记录",
					"conflict": "沈辞不能暴露信息来源",
					"outcome": "两人决定用假名单反向试探",
					"custom_fact": {"evidence": "门禁副本", "risk": 3}
				}
			]
		}]
	}`)
	if err != nil {
		t.Fatalf("decodeOutlineEntries: %v", err)
	}
	if len(entries) != 1 || len(entries[0].Scenes) != 2 {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	structured := entries[0].Scenes[1]
	for _, want := range []string{
		"location: 董事会休息室",
		`characters: ["林舒然","沈辞"]`,
		"goal: 确认谁接触过原始记录",
		"conflict: 沈辞不能暴露信息来源",
		"outcome: 两人决定用假名单反向试探",
		`custom_fact: {"evidence":"门禁副本","risk":3}`,
	} {
		if !strings.Contains(structured, want) {
			t.Fatalf("structured scene %q missing %q", structured, want)
		}
	}
}

func TestDecodeOutlineEntriesAcceptsSingleSceneStringAndArray(t *testing.T) {
	for name, content := range map[string]string{
		"single": `[{"title":"一","core_event":"推进","hook":"悬念","scenes":"完整单场"}]`,
		"array":  `[{"title":"一","core_event":"推进","hook":"悬念","scenes":["场一","场二"]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			entries, err := decodeOutlineEntries("outline", content)
			if err != nil {
				t.Fatalf("decodeOutlineEntries: %v", err)
			}
			if len(entries) != 1 || len(entries[0].Scenes) == 0 {
				t.Fatalf("unexpected entries: %+v", entries)
			}
		})
	}
}

func TestDecodeOutlineEntriesPreservesTemporaryRolesAndBeatAliases(t *testing.T) {
	entries, err := decodeOutlineEntries("expand_arc chapters", `[{
		"chapter": 1,
		"title": "入职",
		"core_event": "苏瑾琛进入刘子昊的同事圈。",
		"hook": "家宴请柬出现。",
		"scenes": ["入职登记", "工位交锋"],
		"character_beats": [{
			"character_id": "su_jinchen",
			"state_advance": "从远端观察进入可触达距离"
		}],
		"relationship_beats": [{
			"relationship_id": "rel_su_liu",
			"source_character_id": "su_jinchen",
			"target_character_id": "liu_zihao",
			"progress": "伪装同事关系成立"
		}],
		"temporary_roles": [
			"维纳斯HR（功能性）",
			{"role":"前台","scene":"入职登记","purpose":"确认工牌","important":false}
		]
	}]`)
	if err != nil {
		t.Fatalf("decodeOutlineEntries: %v", err)
	}
	entry := entries[0]
	if len(entry.TemporaryRoles) != 2 ||
		entry.TemporaryRoles[0].Role != "维纳斯HR（功能性）" ||
		entry.TemporaryRoles[1].Purpose != "确认工牌" {
		t.Fatalf("temporary roles = %+v", entry.TemporaryRoles)
	}
	if len(entry.CharacterBeats) != 1 ||
		entry.CharacterBeats[0].Advance != "从远端观察进入可触达距离" {
		t.Fatalf("character beat aliases = %+v", entry.CharacterBeats)
	}
	if len(entry.RelationshipBeats) != 1 ||
		entry.RelationshipBeats[0].ExpectedAdvance != "伪装同事关系成立" {
		t.Fatalf("relationship beat aliases = %+v", entry.RelationshipBeats)
	}
}

func TestDecodeOutlineEntriesAcceptsSingleTemporaryRoleString(t *testing.T) {
	entries, err := decodeOutlineEntries("outline", `[{
		"chapter":1,
		"title":"家宴",
		"core_event":"请柬送达",
		"hook":"旧识赴宴",
		"scenes":[],
		"temporary_roles":"送件员"
	}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries[0].TemporaryRoles) != 1 || entries[0].TemporaryRoles[0].Role != "送件员" {
		t.Fatalf("temporary roles = %+v", entries[0].TemporaryRoles)
	}
}

func TestDecodeOutlineEntriesFlattensNestedBeatArraysWithoutDataLoss(t *testing.T) {
	entries, err := decodeOutlineEntries("repair_arc chapters", `[{
		"chapter":14,
		"title":"锁链",
		"core_event":"制度升级",
		"hook":"明日起执行",
		"scenes":["场一","场二"],
		"character_beats":[[
			{"character_id":"lin_shuran","scene":"场一","state_advance":"拒绝归属"},
			{"character_id":"su_jinchen","scene":"场二","advance":"升级控制"}
		]],
		"relationship_beats":[[
			{"relationship_id":"rel_su_lin","source_character_id":"su_jinchen","target_character_id":"lin_shuran","progress":"对抗升级"}
		]]
	}]`)
	if err != nil {
		t.Fatalf("decodeOutlineEntries: %v", err)
	}
	if len(entries) != 1 || len(entries[0].CharacterBeats) != 2 {
		t.Fatalf("character beats = %+v", entries)
	}
	if entries[0].CharacterBeats[0].Advance != "拒绝归属" ||
		entries[0].CharacterBeats[1].Advance != "升级控制" {
		t.Fatalf("character beat content changed: %+v", entries[0].CharacterBeats)
	}
	if len(entries[0].RelationshipBeats) != 1 ||
		entries[0].RelationshipBeats[0].ExpectedAdvance != "对抗升级" {
		t.Fatalf("relationship beats = %+v", entries[0].RelationshipBeats)
	}
}

func TestDecodeOutlineEntriesExpandsCompactBeatTuplesWithoutDataLoss(t *testing.T) {
	entries, err := decodeOutlineEntries("repair_arc chapters", `[{
		"chapter":14,
		"title":"锁链",
		"core_event":"制度升级",
		"hook":"明日起执行",
		"scenes":["场一","场二"],
		"character_beats":[
			["lin_shuran","场一","拒绝归属","束缚","承受惩罚","寻找异质出口"],
			["su_jinchen","场二","升级控制","反抗","亲自护理","批准缩权"]
		],
		"relationship_beats":[
			["rel_su_lin","su_jinchen","lin_shuran","场一","强制占有","对抗升级","禁止真情软化"]
		]
	}]`)
	if err != nil {
		t.Fatalf("decodeOutlineEntries: %v", err)
	}
	entry := entries[0]
	if len(entry.CharacterBeats) != 2 ||
		entry.CharacterBeats[0].CharacterID != "lin_shuran" ||
		entry.CharacterBeats[0].Advance != "寻找异质出口" ||
		entry.CharacterBeats[1].ChoiceCost != "亲自护理" {
		t.Fatalf("character beat tuples changed: %+v", entry.CharacterBeats)
	}
	if len(entry.RelationshipBeats) != 1 ||
		entry.RelationshipBeats[0].RelationshipID != "rel_su_lin" ||
		entry.RelationshipBeats[0].ForbiddenJump != "禁止真情软化" {
		t.Fatalf("relationship beat tuple changed: %+v", entry.RelationshipBeats)
	}
}

func TestDecodeOutlineEntriesExpandsCompactTemporaryRoleTuplesWithoutDataLoss(t *testing.T) {
	entries, err := decodeOutlineEntries("repair_arc chapters", `[{
		"chapter":35,
		"title":"封锁",
		"core_event":"安保制度升级",
		"hook":"换岗名单出现",
		"scenes":["门厅封锁","卧室护理"],
		"temporary_roles":[
			["保镖","门厅封锁","执行新门禁",true],
			["护士","卧室护理","完成上药",false]
		]
	}]`)
	if err != nil {
		t.Fatalf("decodeOutlineEntries: %v", err)
	}
	roles := entries[0].TemporaryRoles
	if len(roles) != 2 ||
		roles[0].Role != "保镖" ||
		roles[0].Scene != "门厅封锁" ||
		roles[0].Purpose != "执行新门禁" ||
		!roles[0].Important ||
		roles[1].Role != "护士" ||
		roles[1].Important {
		t.Fatalf("temporary role tuples changed: %+v", roles)
	}
}
