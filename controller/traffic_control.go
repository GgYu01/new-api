package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

type trafficControlUpdateRequest struct {
	common.TrafficControlConfig
	// Revision is the config revision the client based its edit on. When
	// provided and stale, the update is rejected with 409 instead of silently
	// overwriting a concurrent save.
	Revision *uint64 `json:"revision,omitempty"`
}

// GetTrafficControl returns the in-memory runtime snapshot. It deliberately
// performs no SQL or Redis read so refreshing the admin panel is cheap.
func GetTrafficControl(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": common.GetTrafficControlMetrics()})
}

// UpdateTrafficControl persists the complete immutable policy via the
// authoritative submit path and publishes it without a restart. The response
// carries the effective config and revision so the client can adopt the server truth.
func UpdateTrafficControl(c *gin.Context) {
	var req trafficControlUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid traffic control configuration"})
		return
	}
	metrics, err := model.UpdateTrafficControlAuthoritative(req.TrafficControlConfig, req.Revision)
	if err != nil {
		if errors.Is(err, model.ErrTrafficControlConflict) {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "traffic control config was changed concurrently, reload and retry", "data": metrics})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": metrics})
}
