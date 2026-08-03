package host

import (
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func (h *Host) RollbackPreview() (domain.RollbackPreview, error) {
	if h == nil || h.store == nil {
		return domain.RollbackPreview{}, fmt.Errorf("host store is not available")
	}
	return h.store.RollbackPreview()
}

func (h *Host) Rollback(req domain.RollbackRequest) (domain.RollbackResult, error) {
	if h == nil || h.store == nil {
		return domain.RollbackResult{}, fmt.Errorf("host store is not available")
	}
	release, err := h.beginNormalFlowMutation()
	if err != nil {
		return domain.RollbackResult{}, err
	}
	defer release()
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()

	if running {
		h.abortWithEvent("回退前自动暂停当前创作", "warn")
		if h.coordinator != nil {
			h.coordinator.WaitForIdle()
		}
	}

	result, err := h.store.Rollback(req)
	if err != nil {
		return result, err
	}

	h.mu.Lock()
	h.lifecycle = lifecycleIdle
	h.cocreating = false
	h.mu.Unlock()

	h.refreshWriterRestore()
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  "已回退到：" + result.Preview.TargetLabel,
		Level:    "warn",
	})
	return result, nil
}
