package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// GetTrafficControl returns the in-memory runtime snapshot. It deliberately
// performs no SQL or Redis read so refreshing the admin panel is cheap.
func GetTrafficControl(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": common.GetTrafficControlMetrics()})
}

// UpdateTrafficControl persists the complete immutable policy in one option
// transaction and publishes it without a restart.
func UpdateTrafficControl(c *gin.Context) {
	var cfg common.TrafficControlConfig
	if err := common.DecodeJson(c.Request.Body, &cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid traffic control configuration"})
		return
	}
	if err := common.ValidateTrafficControlConfig(cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	values := map[string]string{
		common.TrafficControlEnabledOption:          strconv.FormatBool(cfg.Enabled),
		common.TrafficControlModeOption:             string(cfg.Mode),
		common.TrafficControlGlobalRPMOption:        strconv.FormatInt(cfg.GlobalRPM, 10),
		common.TrafficControlBurstOption:            strconv.FormatInt(cfg.Burst, 10),
		common.TrafficControlMaxActiveOption:        strconv.FormatInt(cfg.MaxActiveRequests, 10),
		common.TrafficControlWaitingQueueOption:     strconv.FormatInt(cfg.WaitingQueue, 10),
		common.TrafficControlWaitingTimeoutMsOption: strconv.FormatInt(cfg.WaitingTimeoutMs, 10),
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := common.SetTrafficControlConfig(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": common.GetTrafficControlMetrics()})
}
