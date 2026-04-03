package main

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	entstoragepolicy "github.com/cloudreve/Cloudreve/v4/ent/storagepolicy"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
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

	const policyName = "__fts_external_minio__"
	policy, err := dep.DBClient().StoragePolicy.Query().Where(entstoragepolicy.NameEQ(policyName)).First(ctx)
	switch {
	case err == nil:
	case ent.IsNotFound(err):
		policy = &ent.StoragePolicy{}
	default:
		panic(err)
	}

	policy.Name = policyName
	policy.Type = string(inventorytypes.PolicyTypeS3)
	policy.Server = "http://127.0.0.1:9000"
	policy.BucketName = "cloudreve-external-smoke"
	policy.IsPrivate = false
	policy.AccessKey = "minio"
	policy.SecretKey = "minio123456"
	policy.MaxSize = 0
	policy.DirNameRule = "uploads/{uid}/{path}"
	policy.FileNameRule = "{uid}_{randomkey8}_{originname}"
	policy.Settings = &inventorytypes.PolicySetting{
		Region:           "us-east-1",
		S3ForcePathStyle: true,
		ChunkSize:        25 << 20,
	}

	policy, err = dep.StoragePolicyClient().Upsert(ctx, policy)
	if err != nil {
		panic(err)
	}

	groupCtx := context.WithValue(ctx, inventory.LoadGroupPolicy{}, true)
	group, err := dep.GroupClient().GetByID(groupCtx, 1)
	if err != nil {
		panic(err)
	}
	group.Edges.StoragePolicies = policy
	group, err = dep.GroupClient().Upsert(ctx, group)
	if err != nil {
		panic(err)
	}

	fmt.Printf("policy_id=%d policy_type=%s bucket=%s group_id=%d\n", policy.ID, policy.Type, policy.BucketName, group.ID)
}
