package simulationcheck

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestScannerDetectsNormalizedLongPhraseRareNGramAndProperNoun(t *testing.T) {
	index := testIndex(
		testEntry("long", "rare_phrase", "银蓝钟摆越过七级石阶后停在逆风里", "report-a"),
		testEntry("ngram", "rare_phrase", "褪色航图压着沉默海风第九枚黄铜羽毛坠入井底", "report-b"),
		testEntry("noun", "proper_noun", "雾灯港", "report-c"),
	)
	draft := "他抵达雾灯港。银蓝钟摆，越过七级石阶后，停在逆风里。" +
		"旧桌上，那张褪色航图压着沉默；稍后第九枚黄铜羽毛坠入井底。"

	risks, err := NewEngine().Scan(context.Background(), draft, index, 40)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	assertRiskType(t, risks, "long_contiguous")
	assertRiskType(t, risks, "rare_ngram")
	assertRiskType(t, risks, "source_specific_term")
	for _, risk := range risks {
		if strings.Contains(risk.DraftExcerpt, "source/") || strings.Contains(risk.DraftExcerpt, "原句") {
			t.Fatalf("risk leaked source material: %+v", risk)
		}
		for _, ref := range risk.SourceRefs {
			if !strings.HasPrefix(ref, "source-") || strings.ContainsAny(ref, `/\`) {
				t.Fatalf("risk leaked unsanitized source reference: %+v", risk)
			}
		}
	}
}

func TestScannerDetectsDistinctiveShortPhraseCombination(t *testing.T) {
	index := testIndex(
		testEntry("short-a", "signature_phrase", "赤雨停", "private/source/a.txt"),
		testEntry("short-b", "rare_phrase", "铜鸟醒", "private/source/a.txt"),
	)
	risks, err := NewEngine().Scan(context.Background(), "赤雨停在窗外，片刻后铜鸟醒了。", index, 30)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	assertRiskType(t, risks, "distinctive_combination")
}

func TestScannerDoesNotBlockCommonShortPhrasesOrCommonTerms(t *testing.T) {
	index := testIndex(
		testEntry("common-one", "proper_noun", "一怔", "report-a"),
		testEntry("common-two", "signature_phrase", "苦笑", "report-a"),
		domain.SimulationSafetyIndexEntry{
			ID: "frequent", Kind: "proper_noun", Value: "旧城区",
			EvidenceRefs: []string{
				"report-1", "report-2", "report-3", "report-4", "report-5",
				"report-6", "report-7", "report-8", "report-9", "report-10",
			},
		},
	)
	risks, err := NewEngine().Scan(context.Background(), "他一怔，随后苦笑着走进旧城区。", index, 20)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(risks) != 0 {
		t.Fatalf("common expressions were blocked: %+v", risks)
	}
}

func TestScannerHonorsCancellationAndInputLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewEngine().Scan(ctx, "text", testIndex(testEntry("x", "proper_noun", "合成专名", "report-a")), 1); err == nil {
		t.Fatal("canceled scan succeeded")
	}
	oversized := strings.Repeat("字", MaxDraftRunes+1)
	if _, err := NewEngine().Scan(context.Background(), oversized, testIndex(testEntry("x", "proper_noun", "合成专名", "report-a")), 1); err == nil {
		t.Fatal("oversized draft scan succeeded")
	}
}

func TestScannerCacheInvalidatesWithSafetyIndexDigest(t *testing.T) {
	engine := NewEngine()
	first := testIndex(testEntry("first", "proper_noun", "雾灯港", "report-a"))
	if risks, err := engine.Scan(context.Background(), "抵达雾灯港", first, 10); err != nil || len(risks) != 1 {
		t.Fatalf("first scan risks=%+v err=%v", risks, err)
	}
	second := testIndex(testEntry("second", "proper_noun", "星砂城", "report-b"))
	if risks, err := engine.Scan(context.Background(), "抵达星砂城", second, 10); err != nil || len(risks) != 1 {
		t.Fatalf("second scan risks=%+v err=%v", risks, err)
	}
	if risks, err := engine.Scan(context.Background(), "抵达雾灯港", second, 10); err != nil || len(risks) != 0 {
		t.Fatalf("stale cached index reused: risks=%+v err=%v", risks, err)
	}
}

func BenchmarkScannerMaximumFixture(b *testing.B) {
	entries := make([]domain.SimulationSafetyIndexEntry, 0, 2_000)
	for i := 0; i < 2_000; i++ {
		entries = append(entries, testEntry(
			fmt.Sprintf("entry-%04d", i),
			"rare_phrase",
			fmt.Sprintf("合成安全短语%04d%s", i, strings.Repeat("甲", 20)),
			"report-a",
		))
	}
	index := testIndex(entries...)
	draft := strings.Repeat("原创段落持续推进人物选择与代价。", 5_000)
	engine := NewEngine()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := engine.Scan(context.Background(), draft, index, 2_000); err != nil {
			b.Fatal(err)
		}
	}
}

func testIndex(entries ...domain.SimulationSafetyIndexEntry) *domain.SimulationSafetyIndex {
	return &domain.SimulationSafetyIndex{
		Version: domain.SimulationSafetyIndexVersion, UpdatedAt: "2026-07-29T00:00:00Z",
		Entries: entries,
	}
}

func testEntry(id, kind, value string, refs ...string) domain.SimulationSafetyIndexEntry {
	return domain.SimulationSafetyIndexEntry{
		ID: id, Kind: kind, Value: value, EvidenceRefs: refs,
	}
}

func assertRiskType(t *testing.T, risks []Risk, want string) {
	t.Helper()
	for _, risk := range risks {
		if risk.Type == want {
			return
		}
	}
	t.Fatalf("risk type %q missing from %+v", want, risks)
}
