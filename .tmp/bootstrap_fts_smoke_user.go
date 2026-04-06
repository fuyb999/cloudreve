package main

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

func main() {
	util.UseWorkingDir = true
	logger := logging.NewConsoleLogger(logging.LevelDebug)
	dep := dependency.NewDependency(
		dependency.WithConfigPath(".tmp/fts_real_smoke.ini"),
		dependency.WithLogger(logger),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())

	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
	ctx = context.WithValue(ctx, inventory.LoadTaskUser{}, true)

	existing, err := dep.UserClient().GetLoginUserByID(ctx, 1)
	if err == nil && existing != nil {
		fmt.Printf("smoke_user_exists id=%d email=%s nick=%s group_id=%d\n", existing.ID, existing.Email, existing.Nick, existing.GroupUsers)
		return
	}

	created, err := dep.UserClient().Create(ctx, &inventory.NewUserArgs{
		RawID:         1,
		Username:      "fts-smoke-admin",
		Email:         "11aoteman@126.com",
		Nick:          "超级管理员",
		PlainPassword: "CloudreveSmoke123!",
		Status:        entuser.StatusActive,
		GroupID:       2,
	})
	if err != nil {
		panic(err)
	}

	fmt.Printf("smoke_user_created id=%d email=%s nick=%s group_id=%d\n", created.ID, created.Email, created.Nick, created.GroupUsers)
}
