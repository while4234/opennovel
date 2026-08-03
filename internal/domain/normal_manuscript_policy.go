package domain

import "fmt"

type NormalManuscriptPolicy struct{}

func (NormalManuscriptPolicy) Identity() (string, string) {
	return NormalManuscriptRevisionPolicyID, ManuscriptRevisionPolicyVersion
}

func (NormalManuscriptPolicy) ValidateBaseline(baseline ManuscriptBaseline) error {
	if baseline.Mode != RevisionModeNormal {
		return fmt.Errorf("normal manuscript policy requires normal mode")
	}
	return baseline.Validate()
}

func (NormalManuscriptPolicy) ValidateCandidate(candidate ManuscriptCandidate) error {
	return candidate.Validate()
}
