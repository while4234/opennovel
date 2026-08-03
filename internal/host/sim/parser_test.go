package sim

import "testing"

func TestParseJSONPayloadRepairsLiteralNewlineInString(t *testing.T) {
	var got struct {
		Summary string `json:"summary"`
	}
	err := parseJSONPayload("{\"summary\":\"第一行\n第二行\"}", &got)
	if err != nil {
		t.Fatalf("parseJSONPayload: %v", err)
	}
	if got.Summary != "第一行\n第二行" {
		t.Fatalf("summary = %q, want literal newline preserved", got.Summary)
	}
}

func TestParseJSONPayloadKeepsNormalMultilineJSON(t *testing.T) {
	var got struct {
		Items []string `json:"items"`
	}
	err := parseJSONPayload("```json\n{\n  \"items\": [\"a\", \"b\"]\n}\n```", &got)
	if err != nil {
		t.Fatalf("parseJSONPayload: %v", err)
	}
	if len(got.Items) != 2 || got.Items[1] != "b" {
		t.Fatalf("items = %+v, want [a b]", got.Items)
	}
}
