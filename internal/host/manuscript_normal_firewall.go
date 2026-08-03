package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

var normalManuscriptSidecarNames = []string{
	"summary",
	"events",
	"timeline",
	"cast_state",
	"relationships",
	"foreshadow",
	"world_facts",
	"carry_forward",
}

type normalManuscriptSchemaError struct {
	err error
}

func (e *normalManuscriptSchemaError) Error() string {
	return "normal manuscript schema: " + e.err.Error()
}

func (e *normalManuscriptSchemaError) Unwrap() error {
	return e.err
}

func newNormalManuscriptSchemaError(format string, args ...any) error {
	return &normalManuscriptSchemaError{err: fmt.Errorf(format, args...)}
}

func isNormalManuscriptSchemaError(err error) bool {
	var schemaErr *normalManuscriptSchemaError
	return errors.As(err, &schemaErr)
}

func validateNormalManuscriptSidecars(payloads map[string]json.RawMessage, requireComplete bool) error {
	allowed := make(map[string]struct{}, len(normalManuscriptSidecarNames))
	for _, name := range normalManuscriptSidecarNames {
		allowed[name] = struct{}{}
	}
	for name, payload := range payloads {
		if normalManuscriptForbiddenKey(name) {
			return newNormalManuscriptSchemaError("sidecar key %q is forbidden on the normal path", name)
		}
		if _, ok := allowed[name]; !ok {
			return newNormalManuscriptSchemaError("unknown top-level sidecar %q", name)
		}
		if err := validateNormalManuscriptJSON(payload); err != nil {
			return fmt.Errorf("sidecar %s: %w", name, err)
		}
	}
	if requireComplete {
		for _, name := range normalManuscriptSidecarNames {
			if _, ok := payloads[name]; !ok {
				return newNormalManuscriptSchemaError("required sidecar %q is missing", name)
			}
		}
	}
	return nil
}

func validateNormalManuscriptJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return newNormalManuscriptSchemaError("invalid JSON: %v", err)
	}
	if err := ensureJSONDocumentEnded(decoder); err != nil {
		return err
	}
	return validateNormalManuscriptValue(value)
}

func validateNormalManuscriptValue(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if normalManuscriptForbiddenKey(key) {
				return newNormalManuscriptSchemaError("field %q is forbidden on the normal path", key)
			}
			if err := validateNormalManuscriptValue(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateNormalManuscriptValue(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalManuscriptForbiddenKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	// RelationshipEntry uses this canonical endpoint on both original and
	// adaptation paths. It identifies a story character, not source material.
	if normalized == "source_character_id" {
		return false
	}
	for _, forbidden := range []string{"source", "adaptation"} {
		if normalized == forbidden || strings.HasPrefix(normalized, forbidden+"_") || strings.HasPrefix(normalized, forbidden+"-") {
			return true
		}
	}
	return false
}

func decodeNormalManuscriptEnvelope(payload []byte, target any) error {
	if err := validateNormalManuscriptJSON(payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return newNormalManuscriptSchemaError("writer envelope: %v", err)
	}
	return ensureJSONDocumentEnded(decoder)
}

func ensureJSONDocumentEnded(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return newNormalManuscriptSchemaError("multiple JSON values are not allowed")
		}
		return newNormalManuscriptSchemaError("invalid trailing JSON: %v", err)
	}
	return nil
}

func validateGeneratedManuscriptSegment(mode domain.RevisionMode, generated ManuscriptGeneratedSegment) error {
	if mode == domain.RevisionModeAdaptation {
		return nil
	}
	return validateNormalManuscriptSidecars(generated.Sidecars, generated.Complete)
}
