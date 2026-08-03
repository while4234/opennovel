package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// UsageStore 持久化 token / cost 累计用量到 meta/usage.json。
// 写入走 IO 的 atomic write（tmp + rename），Save 路径每次完整覆盖整个 state。
type UsageStore struct{ io *IO }

func NewUsageStore(io *IO) *UsageStore { return &UsageStore{io: io} }

// Load 读取 usage.json。文件不存在或 schema 版本不匹配时返回 (nil, nil)，
// 由调用方决定是否走 session replay 一次性回填。
func (s *UsageStore) Load() (*domain.UsageState, error) {
	var state domain.UsageState
	if err := s.io.ReadJSON("meta/usage.json", &state); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if state.Schema == 2 {
		state.Schema = domain.UsageSchemaVersion
		state.LegacyAggregate = true
		state.CoverageUnknown = true
		return &state, nil
	}
	if state.Schema != domain.UsageSchemaVersion {
		return nil, nil
	}
	return &state, nil
}

const usageCallLedgerPath = "meta/usage_calls.jsonl"
const usageDailyAggregatePath = "meta/usage_daily.json"

func (s *UsageStore) AppendCall(event domain.UsageCallEvent) error {
	event.Schema = domain.UsageSchemaVersion
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return s.io.AppendLine(usageCallLedgerPath, append(data, '\n'))
}

func (s *UsageStore) LoadCalls() ([]domain.UsageCallEvent, error) {
	file, err := os.Open(s.io.path(usageCallLedgerPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var calls []domain.UsageCallEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event domain.UsageCallEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		calls = append(calls, event)
	}
	return calls, scanner.Err()
}

// CompactCalls keeps recent call facts and rolls older facts into durable daily aggregates.
func (s *UsageStore) CompactCalls(now time.Time, retention time.Duration) error {
	calls, err := s.LoadCalls()
	if err != nil {
		return err
	}
	cutoff := now.Add(-retention)
	recent := make([]domain.UsageCallEvent, 0, len(calls))
	daily, err := s.LoadDailyAggregates()
	if err != nil {
		return err
	}
	byKey := make(map[string]*domain.UsageDailyAggregate, len(daily))
	for i := range daily {
		item := daily[i]
		byKey[dailyUsageKey(item)] = &item
	}
	for _, call := range calls {
		if !call.Timestamp.Before(cutoff) {
			recent = append(recent, call)
			continue
		}
		item := domain.UsageDailyAggregate{
			Date: call.Timestamp.UTC().Format("2006-01-02"), Provider: call.Provider, Model: call.Model,
			Role: call.Role, Workflow: call.Workflow, Stage: call.Stage,
		}
		key := dailyUsageKey(item)
		total := byKey[key]
		if total == nil {
			total = &item
			byKey[key] = total
		}
		addCallToDaily(total, call)
	}
	if err := s.rewriteCalls(recent); err != nil {
		return err
	}
	daily = daily[:0]
	for _, item := range byKey {
		daily = append(daily, *item)
	}
	sort.Slice(daily, func(i, j int) bool { return dailyUsageKey(daily[i]) < dailyUsageKey(daily[j]) })
	return s.io.WriteJSON(usageDailyAggregatePath, daily)
}

func (s *UsageStore) LoadDailyAggregates() ([]domain.UsageDailyAggregate, error) {
	var daily []domain.UsageDailyAggregate
	if err := s.io.ReadJSON(usageDailyAggregatePath, &daily); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return daily, nil
}

func (s *UsageStore) rewriteCalls(calls []domain.UsageCallEvent) error {
	data := make([]byte, 0, len(calls)*256)
	for _, call := range calls {
		line, err := json.Marshal(call)
		if err != nil {
			return err
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.io.WriteFileUnlocked(usageCallLedgerPath, data)
}

func dailyUsageKey(item domain.UsageDailyAggregate) string {
	return item.Date + "\x00" + item.Provider + "\x00" + item.Model + "\x00" + item.Role + "\x00" + item.Workflow + "\x00" + item.Stage
}

func addCallToDaily(total *domain.UsageDailyAggregate, call domain.UsageCallEvent) {
	total.Calls++
	if call.UsagePresent {
		total.UsageCalls++
	}
	if call.Status != "ok" {
		total.Failures++
	}
	if call.Attempt > 1 {
		total.Retries++
	}
	total.Input += call.Input
	total.Output += call.Output
	total.CacheRead += call.CacheRead
	total.CacheWrite += call.CacheWrite
	total.CostUSD += call.CostUSD
	total.SavedUSD += call.SavedUSD
	total.LatencyMSSum += call.LatencyMS
	if call.FirstReviewPassed != nil {
		total.QualitySamples++
		if *call.FirstReviewPassed {
			total.FirstReviewPasses++
		}
	}
	if call.ConsistencyCheckPassed != nil && *call.ConsistencyCheckPassed {
		total.ConsistencyPasses++
	}
	if call.AdaptationCheckPassed != nil && *call.AdaptationCheckPassed {
		total.AdaptationPasses++
	}
	if call.ReviewScore != nil {
		total.ReviewScoreSum += *call.ReviewScore
	}
	total.AcceptedRunes += call.AcceptedRunes
}

// Save 把 state 完整覆盖落盘。调用方负责 debounce / 节流。
func (s *UsageStore) Save(state domain.UsageState) error {
	state.Schema = domain.UsageSchemaVersion
	return s.io.WriteJSON("meta/usage.json", state)
}
