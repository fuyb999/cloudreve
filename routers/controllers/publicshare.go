package controllers

import (
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	publicsvc "github.com/cloudreve/Cloudreve/v4/service/publicshare"
	"github.com/gin-gonic/gin"
)

func PublicRemoteVisibility(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.RemoteVisibilityService](c, publicsvc.RemoteVisibilityParamCtx{})
	res, err := service.Get(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func PublicRemoteCheck(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.RemoteCheckService](c, publicsvc.RemoteCheckParamCtx{})
	res, err := service.Check(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func PublicGetResource(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.PublicResourceService](c, publicsvc.PublicResourceParamCtx{})
	res, err := service.Get(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func PublicListResourceChildren(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.PublicChildrenService](c, publicsvc.PublicChildrenParamCtx{})
	res, err := service.List(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminGetPublicRoot(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.AdminPublicRootService](c, publicsvc.AdminPublicRootParamCtx{})
	res, err := service.Get(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminEnsurePublicRoot(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.AdminPublicRootService](c, publicsvc.AdminPublicRootParamCtx{})
	res, err := service.Ensure(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminGetPublicResource(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.AdminPublicResourceService](c, publicsvc.AdminPublicResourceParamCtx{})
	res, err := service.Get(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminListPublicResourceChildren(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.AdminPublicChildrenService](c, publicsvc.AdminPublicChildrenParamCtx{})
	res, err := service.List(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminListPublicFolders(c *gin.Context) {
	res, err := publicsvc.ListPublicFolders(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminCreatePublicFolder(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.AdminPublicFolderCreateService](c, publicsvc.AdminPublicFolderCreateParamCtx{})
	res, err := service.Create(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminUpdatePublicFolderRule(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.AdminPublicFolderRuleService](c, publicsvc.AdminPublicFolderRuleParamCtx{})
	res, err := service.Update(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminGetPublicMockState(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.AdminPublicMockStateService](c, publicsvc.AdminPublicMockStateParamCtx{})
	res, err := service.Get(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminUpdatePublicMockState(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.AdminPublicMockStateService](c, publicsvc.AdminPublicMockStateParamCtx{})
	res, err := service.Update(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}

func AdminUpsertPublicProfile(c *gin.Context) {
	service := ParametersFromContext[*publicsvc.AdminPublicProfileService](c, publicsvc.AdminPublicProfileParamCtx{})
	res, err := service.Upsert(c)
	if err != nil {
		c.JSON(200, serializer.Err(c, err))
		c.Abort()
		return
	}

	c.JSON(200, serializer.Response{Data: res})
}
