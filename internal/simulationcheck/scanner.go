package simulationcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/voocel/ainovel-cli/internal/domain"
	"golang.org/x/text/unicode/norm"
)

const (
	MaxDraftRunes      = 200_000
	MaxIndexEntries    = 8_192
	MaxIndexedRunes    = 2_000_000
	LongMatchRunes     = 16
	RareNGramRunes     = 8
	RareNGramHits      = 2
	ProperNounMinRunes = 3
	SignatureMinRunes  = 6
	MaxReportedRisks   = 32
)

var commonSurfaceAllowlist = map[string]struct{}{
	"一怔": {}, "苦笑": {}, "点头": {}, "摇头": {}, "沉默": {}, "叹气": {},
	"皱眉": {}, "抬头": {}, "低声": {}, "缓缓": {}, "忽然": {}, "然而": {},
}

type Engine struct {
	mu       sync.Mutex
	cacheKey string
	compiled *compiledIndex
}

type compiledIndex struct {
	entries []compiledEntry
}

type compiledEntry struct {
	id      string
	kind    string
	value   []rune
	refs    []string
	support int
	rare    bool
	ngrams  []string
}

type combinationMatch struct {
	entry  compiledEntry
	start  int
	length int
}

type normalizedText struct {
	runes     []rune
	original  []rune
	positions []int
}

func NewEngine() *Engine { return &Engine{} }

func ConfigurationDigest() string {
	payload := struct {
		Version         string
		MaxDraftRunes   int
		MaxIndexEntries int
		MaxIndexedRunes int
		LongMatchRunes  int
		RareNGramRunes  int
		RareNGramHits   int
		ProperNounRunes int
		SignatureRunes  int
		Allowlist       []string
	}{
		Version: CheckerVersion, MaxDraftRunes: MaxDraftRunes,
		MaxIndexEntries: MaxIndexEntries, MaxIndexedRunes: MaxIndexedRunes,
		LongMatchRunes: LongMatchRunes, RareNGramRunes: RareNGramRunes,
		RareNGramHits: RareNGramHits, ProperNounRunes: ProperNounMinRunes,
		SignatureRunes: SignatureMinRunes,
	}
	for value := range commonSurfaceAllowlist {
		payload.Allowlist = append(payload.Allowlist, value)
	}
	sort.Strings(payload.Allowlist)
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func SafetyIndexDigest(index *domain.SimulationSafetyIndex) string {
	if index == nil {
		return ""
	}
	data, _ := json.Marshal(index)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (e *Engine) Scan(ctx context.Context, draft string, index *domain.SimulationSafetyIndex, sourceCount int) ([]Risk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := normalizeWithPositions(draft)
	if len(text.original) > MaxDraftRunes {
		return nil, fmt.Errorf("simulation draft exceeds %d rune scan limit", MaxDraftRunes)
	}
	if index == nil || len(index.Entries) == 0 {
		return nil, nil
	}
	compiled, err := e.compile(index, sourceCount)
	if err != nil {
		return nil, err
	}
	var risks []Risk
	seen := make(map[string]struct{})
	normalizedDraft := string(text.runes)
	var combinations []combinationMatch
	for entryIndex, entry := range compiled.entries {
		if entryIndex%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		matchStart, matchLength, riskType := matchEntry(normalizedDraft, entry)
		if combinationEligible(entry) {
			if exactStart := indexNormalized(normalizedDraft, string(entry.value)); exactStart >= 0 {
				combinations = append(combinations, combinationMatch{
					entry: entry, start: exactStart, length: len(entry.value),
				})
			}
		}
		if matchStart < 0 {
			continue
		}
		risk := buildRisk(text, entry, matchStart, matchLength, riskType)
		if _, exists := seen[risk.ID]; exists {
			continue
		}
		seen[risk.ID] = struct{}{}
		risks = append(risks, risk)
		if len(risks) >= MaxReportedRisks {
			break
		}
	}
	for _, risk := range combinationRisks(text, combinations, MaxReportedRisks-len(risks)) {
		if _, exists := seen[risk.ID]; exists {
			continue
		}
		seen[risk.ID] = struct{}{}
		risks = append(risks, risk)
	}
	sort.Slice(risks, func(i, j int) bool {
		if risks[i].StartRune != risks[j].StartRune {
			return risks[i].StartRune < risks[j].StartRune
		}
		return risks[i].ID < risks[j].ID
	})
	return risks, nil
}

func (e *Engine) compile(index *domain.SimulationSafetyIndex, sourceCount int) (*compiledIndex, error) {
	key := SafetyIndexDigest(index) + fmt.Sprintf(":%d", sourceCount)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cacheKey == key && e.compiled != nil {
		return e.compiled, nil
	}
	if len(index.Entries) > MaxIndexEntries {
		return nil, fmt.Errorf("simulation safety index exceeds %d entry scan limit", MaxIndexEntries)
	}
	compiled := &compiledIndex{entries: make([]compiledEntry, 0, len(index.Entries))}
	totalRunes := 0
	for _, item := range index.Entries {
		value := normalizeRunes(item.Value)
		if len(value) == 0 || allowlisted(value) {
			continue
		}
		totalRunes += len(value)
		if totalRunes > MaxIndexedRunes {
			return nil, fmt.Errorf("simulation safety index exceeds %d normalized rune scan limit", MaxIndexedRunes)
		}
		support := len(item.EvidenceRefs)
		rareLimit := 2
		if sourceCount > 20 {
			rareLimit = max(2, sourceCount/10)
		}
		entry := compiledEntry{
			id: item.ID, kind: item.Kind, value: value,
			refs:    sanitizedSourceRefs(item.EvidenceRefs),
			support: support, rare: support <= rareLimit,
		}
		if entry.rare && len(value) >= RareNGramRunes+4 {
			seen := make(map[string]struct{})
			for i := 0; i+RareNGramRunes <= len(value); i++ {
				gram := string(value[i : i+RareNGramRunes])
				if _, exists := seen[gram]; !exists {
					entry.ngrams = append(entry.ngrams, gram)
					seen[gram] = struct{}{}
				}
			}
		}
		compiled.entries = append(compiled.entries, entry)
	}
	e.cacheKey, e.compiled = key, compiled
	return compiled, nil
}

func combinationEligible(entry compiledEntry) bool {
	if !entry.rare || len(entry.value) < 3 || len(entry.value) >= SignatureMinRunes {
		return false
	}
	return entry.kind == "rare_phrase" || entry.kind == "signature_phrase"
}

func combinationRisks(text normalizedText, matches []combinationMatch, limit int) []Risk {
	if limit <= 0 || len(matches) < 2 {
		return nil
	}
	byRef := make(map[string][]combinationMatch)
	for _, match := range matches {
		for _, ref := range match.entry.refs {
			byRef[ref] = append(byRef[ref], match)
		}
	}
	refs := make([]string, 0, len(byRef))
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	var risks []Risk
	for _, ref := range refs {
		group := byRef[ref]
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].start < group[j].start })
		first, second := group[0], group[1]
		if first.entry.id == second.entry.id {
			continue
		}
		firstExcerpt, firstStart, firstEnd := draftExcerpt(text, first.start, first.length)
		secondExcerpt, _, secondEnd := draftExcerpt(text, second.start, second.length)
		excerpt := firstExcerpt
		if second.start > first.start+first.length {
			excerpt += " … " + secondExcerpt
		}
		sum := sha256.Sum256([]byte(strings.Join([]string{
			first.entry.id, second.entry.id, ref, excerpt,
		}, "\x00")))
		risks = append(risks, Risk{
			ID: "risk-" + hex.EncodeToString(sum[:8]), Type: "distinctive_combination",
			Severity: "blocking", DraftExcerpt: excerpt, StartRune: firstStart,
			LengthRunes: max(firstEnd, secondEnd) - firstStart,
			SourceRefs:  []string{ref}, EvidenceSupport: 1,
		})
		if len(risks) >= limit {
			break
		}
	}
	return risks
}

func matchEntry(draft string, entry compiledEntry) (int, int, string) {
	if start := indexNormalized(draft, string(entry.value)); start >= 0 {
		switch {
		case len(entry.value) >= LongMatchRunes:
			return start, len(entry.value), "long_contiguous"
		case entry.kind == "proper_noun" && entry.rare && len(entry.value) >= ProperNounMinRunes:
			return start, len(entry.value), "source_specific_term"
		case entry.kind == "signature_phrase" && entry.rare && len(entry.value) >= SignatureMinRunes:
			return start, len(entry.value), "signature_phrase"
		case entry.kind == "rare_phrase" && entry.rare && len(entry.value) >= SignatureMinRunes:
			return start, len(entry.value), "rare_phrase"
		}
	}
	if !entry.rare || len(entry.ngrams) == 0 {
		return -1, 0, ""
	}
	hits := make([]int, 0, RareNGramHits)
	for _, gram := range entry.ngrams {
		if start := indexNormalized(draft, gram); start >= 0 {
			hits = append(hits, start)
			if len(hits) >= RareNGramHits {
				sort.Ints(hits)
				return hits[0], RareNGramRunes, "rare_ngram"
			}
		}
	}
	return -1, 0, ""
}

func buildRisk(text normalizedText, entry compiledEntry, normalizedStart, normalizedLength int, riskType string) Risk {
	excerpt, start, end := draftExcerpt(text, normalizedStart, normalizedLength)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		entry.id, riskType, fmt.Sprint(start), fmt.Sprint(end), excerpt,
	}, "\x00")))
	return Risk{
		ID: "risk-" + hex.EncodeToString(sum[:8]), Type: riskType, Severity: "blocking",
		DraftExcerpt: excerpt, StartRune: start, LengthRunes: end - start,
		SourceRefs: append([]string(nil), entry.refs...), EvidenceSupport: entry.support,
	}
}

func draftExcerpt(text normalizedText, normalizedStart, normalizedLength int) (string, int, int) {
	start := text.positions[normalizedStart]
	endPosition := normalizedStart + normalizedLength - 1
	end := text.positions[endPosition] + 1
	if end > len(text.original) {
		end = len(text.original)
	}
	excerptStart := max(0, start-8)
	excerptEnd := min(len(text.original), end+8)
	return string(text.original[excerptStart:excerptEnd]), start, end
}

func sanitizedSourceRefs(refs []string) []string {
	out := make([]string, 0, min(len(refs), 8))
	seen := make(map[string]struct{})
	for _, ref := range refs {
		sum := sha256.Sum256([]byte(ref))
		safe := "source-" + hex.EncodeToString(sum[:12])
		if _, exists := seen[safe]; exists {
			continue
		}
		seen[safe] = struct{}{}
		out = append(out, safe)
		if len(out) >= 8 {
			break
		}
	}
	sort.Strings(out)
	return out
}

func normalizeWithPositions(value string) normalizedText {
	original := []rune(value)
	out := normalizedText{original: original}
	for position, r := range original {
		for _, normalized := range []rune(norm.NFKC.String(string(r))) {
			if unicode.IsSpace(normalized) || unicode.IsPunct(normalized) || unicode.IsSymbol(normalized) ||
				unicode.Is(unicode.Cf, normalized) {
				continue
			}
			out.runes = append(out.runes, unicode.ToLower(normalized))
			out.positions = append(out.positions, position)
		}
	}
	return out
}

func normalizeRunes(value string) []rune {
	return normalizeWithPositions(value).runes
}

func allowlisted(value []rune) bool {
	_, ok := commonSurfaceAllowlist[string(value)]
	return ok
}

func indexNormalized(haystack, needle string) int {
	if needle == "" || len(needle) > len(haystack) {
		return -1
	}
	byteIndex := strings.Index(haystack, needle)
	if byteIndex < 0 {
		return -1
	}
	return len([]rune(haystack[:byteIndex]))
}
