package admin

import (
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SetModelDegradationProbeService attaches the optional model degradation probe service.
func (h *AccountHandler) SetModelDegradationProbeService(probe *service.ModelDegradationProbeService) {
	h.modelDegradationProbe = probe
}

// ProbeModelDegradation runs one model degradation probe immediately.
// POST /api/v1/admin/accounts/:id/model-degradation-probe
func (h *AccountHandler) ProbeModelDegradation(c *gin.Context) {
	if h.modelDegradationProbe == nil {
		response.ErrorFrom(c, service.ErrModelDegradationProbeUnavailable)
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	snapshot, err := h.modelDegradationProbe.ProbeAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"account_id": accountID, "snapshot": snapshot})
}

type modelDegradationProbeBatchRequest struct {
	AccountIDs []int64 `json:"account_ids"`
}

func (h *AccountHandler) bindModelDegradationProbeAccountIDs(c *gin.Context) ([]int64, bool) {
	var req modelDegradationProbeBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return nil, false
	}
	if len(req.AccountIDs) == 0 || len(req.AccountIDs) > service.ModelDegradationProbeMaxBatchSize {
		response.BadRequest(c, fmt.Sprintf("account_ids must contain between 1 and %d items", service.ModelDegradationProbeMaxBatchSize))
		return nil, false
	}
	seen := make(map[int64]struct{}, len(req.AccountIDs))
	accountIDs := make([]int64, 0, len(req.AccountIDs))
	for _, accountID := range req.AccountIDs {
		if accountID <= 0 {
			response.BadRequest(c, "account_ids must contain positive IDs")
			return nil, false
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		accountIDs = append(accountIDs, accountID)
	}
	return accountIDs, true
}

// ProbeModelDegradationBatch queues model degradation probes for a batch of accounts.
// POST /api/v1/admin/accounts/model-degradation-probe/batch
func (h *AccountHandler) ProbeModelDegradationBatch(c *gin.Context) {
	if h.modelDegradationProbe == nil {
		response.ErrorFrom(c, service.ErrModelDegradationProbeUnavailable)
		return
	}
	accountIDs, ok := h.bindModelDegradationProbeAccountIDs(c)
	if !ok {
		return
	}
	result, err := h.modelDegradationProbe.StartBatchProbe(c.Request.Context(), accountIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// GetModelDegradationSnapshots returns the latest probe snapshots for the given accounts.
// POST /api/v1/admin/accounts/model-degradation-probe/snapshots
func (h *AccountHandler) GetModelDegradationSnapshots(c *gin.Context) {
	if h.modelDegradationProbe == nil {
		response.ErrorFrom(c, service.ErrModelDegradationProbeUnavailable)
		return
	}
	accountIDs, ok := h.bindModelDegradationProbeAccountIDs(c)
	if !ok {
		return
	}
	items, err := h.modelDegradationProbe.GetSnapshots(c.Request.Context(), accountIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}
