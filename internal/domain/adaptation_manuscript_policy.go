package domain

import "fmt"

type AdaptationManuscriptPolicy struct{}

func (AdaptationManuscriptPolicy) Identity() (string, string) {
	return AdaptationManuscriptRevisionPolicyID, ManuscriptRevisionPolicyVersion
}

func (AdaptationManuscriptPolicy) ValidateBaseline(baseline ManuscriptBaseline) error {
	if baseline.Mode != RevisionModeAdaptation {
		return fmt.Errorf("adaptation manuscript policy requires adaptation mode")
	}
	return baseline.Validate()
}

func (AdaptationManuscriptPolicy) ValidateCandidate(candidate ManuscriptCandidate) error {
	return candidate.Validate()
}
