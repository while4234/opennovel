package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type acceptedExpansionSource struct {
	RevisionID       string
	Mode             domain.RevisionMode
	PreviewSignature string
	Origin           domain.ExpansionOrigin
	Facts            domain.ExpansionDramaticFactSet
}

// ValidateRevisionStateForClone admits only a quiescent, internally valid
// accepted-publication journal. Process leases, command capabilities and
// in-flight publication tokens are never copied into a validation clone.
func ValidateRevisionStateForClone(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state revisionState
	if err := decoder.Decode(&state); err != nil {
		return err
	}
	if err := ensureSingleJSONValue(decoder); err != nil {
		return err
	}
	if err := validateRevisionState(&state); err != nil {
		return err
	}
	if state.NormalLease != nil || state.CommandFence != nil || state.Publication != nil || state.ActiveSessionID != "" {
		return fmt.Errorf("revision journal is not quiescent")
	}
	return nil
}

func ensureSingleJSONValue(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("revision journal contains multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func validExpansionPublicationPolicy(session domain.RevisionSession) bool {
	switch session.Mode {
	case domain.RevisionModeNormal:
		return session.PolicyID == domain.NormalRevisionPolicyID && session.PolicyVersion == domain.NormalRevisionPolicyVersion
	case domain.RevisionModeAdaptation:
		return session.PolicyID == domain.AdaptationRevisionPolicyID && session.PolicyVersion == domain.AdaptationRevisionPolicyVersion
	default:
		return false
	}
}
