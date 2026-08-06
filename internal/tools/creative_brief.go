package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const creativeBriefUsage = "这是用户已确认的最高优先级故事事实。人物姓名、身份、地点、核心关系和主线必须以此为准；仿写画像只能提供抽象写法，冲突时必须舍弃画像内容。"

var (
	creativeBriefNovelNamePattern = regexp.MustCompile(`(?m)^\s*-\s*书名\s*[：:]\s*《([^》\r\n]+)》`)
	creativeBriefLeadPattern      = regexp.MustCompile(`(?m)^\s*-\s*(女主|男主)\s+([^\s：:（(，,；;\r\n]{2,12})\s*[：:]`)
	creativeBriefSettingPattern   = regexp.MustCompile(`(?m)^\s*-\s*地点\s*[：:]\s*([^\s（(，,；;\r\n]+)`)
	creativeBriefRelationPattern  = regexp.MustCompile(`(?m)^\s*-\s*关系关键词\s*[：:]\s*([^\r\n]+)`)
)

type creativeBriefIdentity struct {
	NovelName           string            `json:"novel_name,omitempty"`
	Protagonists        map[string]string `json:"protagonists,omitempty"`
	PrimarySetting      string            `json:"primary_setting,omitempty"`
	RelationshipAnchors []string          `json:"relationship_anchors,omitempty"`
}

type creativeBriefContract struct {
	Source        string                `json:"source"`
	Authority     string                `json:"authority"`
	Content       string                `json:"content"`
	IdentityLocks creativeBriefIdentity `json:"identity_locks"`
	Usage         string                `json:"usage"`
}

func newCreativeBriefContract(brief string) *creativeBriefContract {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return nil
	}
	return &creativeBriefContract{
		Source:        "planning_review.brief",
		Authority:     "canonical_user_confirmed",
		Content:       brief,
		IdentityLocks: parseCreativeBriefIdentity(brief),
		Usage:         creativeBriefUsage,
	}
}

func (t *ContextTool) compactEstablishedCreativeBrief(result map[string]any) bool {
	foundation, err := t.store.Foundation.Load()
	if err != nil || foundation.Revision <= 0 || strings.TrimSpace(foundation.Premise) == "" {
		return false
	}
	planning, _ := result["planning_memory"].(map[string]any)
	if planning == nil {
		return false
	}
	contract, ok := planning["creative_brief"].(*creativeBriefContract)
	if !ok || contract == nil || strings.TrimSpace(contract.Content) == "" {
		return false
	}
	digest := sha256.Sum256([]byte(contract.Content))
	contentDigest := hex.EncodeToString(digest[:])
	contentSource := "canonical Foundation artifacts derived from this approved brief"
	planning["creative_brief"] = map[string]any{
		"source":         contract.Source,
		"authority":      contract.Authority,
		"identity_locks": contract.IdentityLocks,
		"usage":          contract.Usage,
		"content_digest": contentDigest,
		"content_source": contentSource,
	}
	return contentDigest != "" && contentSource != ""
}

func parseCreativeBriefIdentity(brief string) creativeBriefIdentity {
	identity := creativeBriefIdentity{}
	if match := creativeBriefNovelNamePattern.FindStringSubmatch(brief); len(match) == 2 {
		identity.NovelName = strings.TrimSpace(match[1])
	}
	for _, match := range creativeBriefLeadPattern.FindAllStringSubmatch(brief, -1) {
		if len(match) != 3 {
			continue
		}
		if identity.Protagonists == nil {
			identity.Protagonists = make(map[string]string, 2)
		}
		identity.Protagonists[strings.TrimSpace(match[1])] = strings.TrimSpace(match[2])
	}
	if match := creativeBriefSettingPattern.FindStringSubmatch(brief); len(match) == 2 {
		identity.PrimarySetting = strings.TrimSpace(match[1])
	}
	if match := creativeBriefRelationPattern.FindStringSubmatch(brief); len(match) == 2 {
		identity.RelationshipAnchors = splitCreativeBriefAnchors(match[1])
	}
	return identity
}

func splitCreativeBriefAnchors(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return strings.ContainsRune("、，,＋+/；;", r)
	})
	anchors := make([]string, 0, min(len(parts), 8))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || slices.Contains(anchors, part) {
			continue
		}
		anchors = append(anchors, part)
		if len(anchors) == 8 {
			break
		}
	}
	return anchors
}

func validatePremiseAgainstCreativeBrief(premise, brief string) error {
	identity := parseCreativeBriefIdentity(brief)
	missing := make([]string, 0, 4)
	if identity.NovelName != "" && !strings.Contains(premise, identity.NovelName) {
		missing = append(missing, "书名《"+identity.NovelName+"》")
	}
	for _, role := range []string{"女主", "男主"} {
		if name := identity.Protagonists[role]; name != "" && !strings.Contains(premise, name) {
			missing = append(missing, role+" "+name)
		}
	}
	if identity.PrimarySetting != "" && !strings.Contains(premise, identity.PrimarySetting) {
		missing = append(missing, "地点 "+identity.PrimarySetting)
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"premise conflicts with the canonical co-create brief; missing identity locks: %s. Regenerate from planning_memory.creative_brief and treat simulation_profile as technique-only",
		strings.Join(missing, "、"),
	)
}

func validateCharactersAgainstCreativeBrief(characters []domain.Character, brief string) error {
	identity := parseCreativeBriefIdentity(brief)
	if len(identity.Protagonists) == 0 {
		return nil
	}
	names := make(map[string]struct{}, len(characters))
	for _, character := range characters {
		names[strings.TrimSpace(character.Name)] = struct{}{}
	}
	missing := make([]string, 0, 2)
	for _, role := range []string{"女主", "男主"} {
		name := identity.Protagonists[role]
		if name == "" {
			continue
		}
		if _, ok := names[name]; !ok {
			missing = append(missing, role+" "+name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"characters conflict with the canonical co-create brief; missing canonical protagonists: %s. Keep these exact names and use simulation_profile only for abstract technique",
		strings.Join(missing, "、"),
	)
}
