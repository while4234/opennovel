package adaptaudit

import (
	"fmt"
	"strings"
)

func ValidateConfirmationRequest(report Report, request ConfirmationRequest) error {
	if err := ValidateReportDigest(report); err != nil {
		return err
	}
	if report.Status != "fail" || !report.Confirmation.Required || len(report.Confirmation.BlockingFindingIDs) == 0 {
		return fmt.Errorf("audit report is not eligible for automatic repair")
	}
	if strings.TrimSpace(request.ReportDigest) == "" || request.ReportDigest != report.Digest {
		return fmt.Errorf("stale or missing report digest")
	}
	if request.Decision != "apply" {
		return fmt.Errorf("decision must be apply")
	}
	acknowledged := make(map[string]bool, len(request.AcknowledgedFindingIDs))
	for _, id := range request.AcknowledgedFindingIDs {
		acknowledged[id] = true
	}
	for _, id := range report.Confirmation.BlockingFindingIDs {
		if !acknowledged[id] {
			return fmt.Errorf("blocking finding %s has not been acknowledged", id)
		}
	}
	return nil
}
