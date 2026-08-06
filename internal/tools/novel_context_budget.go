package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

var planningContextLimits = map[string]int{
	"planning":        planningContextSourceBytes,
	"planning_volume": planningVolumeContextSourceBytes,
	"planning_detail": planningDetailContextSourceBytes,
	"planning_review": planningReviewContextSourceBytes,
	"planning_audit":  planningAuditContextSourceBytes,
}

func marshalBoundedContext(scope string, result map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	limit, bounded := planningContextLimits[scope]
	if !bounded || len(raw) <= limit {
		return raw, nil
	}
	return nil, fmt.Errorf(
		"%s context is %d bytes, exceeds %d-byte final JSON budget; section bytes: %s",
		scope,
		len(raw),
		limit,
		planningContextSectionSizes(result),
	)
}

func planningContextSectionSizes(result map[string]any) string {
	type sectionSize struct {
		name string
		size int
	}
	sections := make([]sectionSize, 0, len(result))
	for name, value := range result {
		raw, err := json.Marshal(value)
		if err != nil {
			continue
		}
		sections = append(sections, sectionSize{name: name, size: len(raw)})
	}
	sort.Slice(sections, func(i, j int) bool {
		if sections[i].size == sections[j].size {
			return sections[i].name < sections[j].name
		}
		return sections[i].size > sections[j].size
	})
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		parts = append(parts, fmt.Sprintf("%s=%d", section.name, section.size))
	}
	return strings.Join(parts, ", ")
}
