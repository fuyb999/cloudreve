package controllers

import (
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/service/setting"
	"github.com/gin-gonic/gin"
)

func ListSyncthingDevices(c *gin.Context) {
	service := ParametersFromContext[*setting.ListSyncthingDevicesService](c, setting.ListSyncthingDevicesParamCtx{})
	resp, err := service.List(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{
		Data: resp,
	})
}

func UpsertSyncthingDevice(c *gin.Context) {
	service := ParametersFromContext[*setting.UpsertSyncthingDeviceService](c, setting.UpsertSyncthingDeviceParamCtx{})
	resp, err := service.Upsert(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{
		Data: resp,
	})
}

func UnbindSyncthingDevice(c *gin.Context) {
	service := ParametersFromContext[*setting.DeleteSyncthingDeviceService](c, setting.DeleteSyncthingDeviceParamCtx{})
	if err := service.Unbind(c); err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{})
}

func DeleteSyncthingDevice(c *gin.Context) {
	service := ParametersFromContext[*setting.DeleteSyncthingDeviceService](c, setting.DeleteSyncthingDeviceParamCtx{})
	if err := service.Delete(c); err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{})
}

func SyncthingDeviceHeartbeat(c *gin.Context) {
	service := ParametersFromContext[*setting.SyncthingHeartbeatService](c, setting.SyncthingHeartbeatParamCtx{})
	resp, err := service.Heartbeat(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{
		Data: resp,
	})
}

func SyncthingDeviceActivity(c *gin.Context) {
	service := ParametersFromContext[*setting.SyncthingActivityService](c, setting.SyncthingActivityParamCtx{})
	resp, err := service.Report(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{
		Data: resp,
	})
}
