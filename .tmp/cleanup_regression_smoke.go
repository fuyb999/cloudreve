package main

import (
	"context"
	"fmt"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	ententity "github.com/cloudreve/Cloudreve/v4/ent/entity"
	entfile "github.com/cloudreve/Cloudreve/v4/ent/file"
	entmetadata "github.com/cloudreve/Cloudreve/v4/ent/metadata"
	"github.com/cloudreve/Cloudreve/v4/ent/predicate"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
)

const cleanupSmokeUserID = 1

var cleanupPrefixes = []string{
	"__fts_real_smoke_",
	"__fts_external_modes_",
	"__media_meta_smoke_",
}

func main() {
	util.UseWorkingDir = true

	dep := dependency.NewDependency(
		dependency.WithConfigPath(".tmp/fts_real_smoke.ini"),
		dependency.WithLogger(logging.NewConsoleLogger(logging.LevelInformational)),
		dependency.WithRequiredDbVersion(constants.BackendVersion),
	)
	defer dep.Shutdown(context.Background())

	ctx := context.WithValue(context.Background(), dependency.DepCtx{}, dep)
	ctx = context.WithValue(ctx, inventory.LoadTaskUser{}, true)

	user, err := dep.UserClient().GetLoginUserByID(ctx, cleanupSmokeUserID)
	must(err, "load cleanup user")
	ctx = context.WithValue(ctx, inventory.UserCtx{}, user)
	ctx = context.WithValue(ctx, inventory.UserIDCtx{}, user.ID)

	fm := manager.NewFileManager(dep, user)
	defer fm.Recycle()

	files, err := queryPrefixNamedFiles(ctx, dep, user.ID)
	must(err, "query smoke files")
	fmt.Printf("cleanup root file candidates=%d\n", len(files))
	deleteFiles(ctx, fm, files, "cloudreve://my/", "root")

	trashFiles, err := querySmokeTrashFiles(ctx, dep, user.ID)
	must(err, "query smoke trash files")
	fmt.Printf("cleanup trash file candidates=%d\n", len(trashFiles))
	deleteFiles(ctx, fm, trashFiles, "cloudreve://trash/", "trash")

	entities, err := querySmokeEntities(ctx, dep)
	must(err, "query smoke entities")
	fmt.Printf("cleanup stale entity candidates=%d\n", len(entities))
	for start := 0; start < len(entities); start += 50 {
		end := start + 50
		if end > len(entities) {
			end = len(entities)
		}
		ids := make([]int, 0, end-start)
		for _, e := range entities[start:end] {
			ids = append(ids, e.ID)
			fmt.Printf("cleanup recycle entity_id=%d source=%s\n", e.ID, e.Source)
		}
		if err := fm.RecycleEntities(ctx, false, ids...); err != nil {
			panic(fmt.Sprintf("recycle entities %v: %v", ids, err))
		}
	}
}

func must(err error, action string) {
	if err != nil {
		panic(fmt.Sprintf("%s: %v", action, err))
	}
}

func queryPrefixNamedFiles(ctx context.Context, dep dependency.Dep, userID int) ([]*ent.File, error) {
	predicates := make([]predicate.File, 0, len(cleanupPrefixes))
	for _, prefix := range cleanupPrefixes {
		predicates = append(predicates, entfile.NameHasPrefix(prefix))
	}

	return dep.DBClient().File.Query().
		Where(entfile.OwnerIDEQ(userID), entfile.Or(predicates...)).
		Order(ent.Asc(entfile.FieldID)).
		All(ctx)
}

func querySmokeTrashFiles(ctx context.Context, dep dependency.Dep, userID int) ([]*ent.File, error) {
	valuePredicates := make([]predicate.Metadata, 0, len(cleanupPrefixes))
	for _, prefix := range cleanupPrefixes {
		valuePredicates = append(valuePredicates, entmetadata.ValueContains(prefix))
	}

	return dep.DBClient().File.Query().
		Where(
			entfile.OwnerIDEQ(userID),
			entfile.HasMetadataWith(
				entmetadata.NameEQ(dbfs.MetadataRestoreUri),
				entmetadata.Or(valuePredicates...),
			),
		).
		Order(ent.Asc(entfile.FieldID)).
		All(ctx)
}

func querySmokeEntities(ctx context.Context, dep dependency.Dep) ([]*ent.Entity, error) {
	predicates := make([]predicate.Entity, 0, len(cleanupPrefixes))
	for _, prefix := range cleanupPrefixes {
		predicates = append(predicates, ententity.SourceContains(prefix))
	}

	return dep.DBClient().Entity.Query().
		Where(ententity.Or(predicates...)).
		Order(ent.Asc(ententity.FieldID)).
		All(ctx)
}

func deleteFiles(ctx context.Context, fm manager.FileManager, files []*ent.File, prefix, label string) {
	uris := make([]*fs.URI, 0, len(files))
	for _, f := range files {
		uri, err := fs.NewUriFromString(prefix + f.Name)
		must(err, "build cleanup uri")
		uris = append(uris, uri)
		fmt.Printf("cleanup %s target file_id=%d name=%s\n", label, f.ID, f.Name)
	}

	for start := 0; start < len(uris); start += 10 {
		end := start + 10
		if end > len(uris) {
			end = len(uris)
		}
		fmt.Printf("cleanup %s deleting batch=%d..%d\n", label, start, end-1)
		if err := fm.Delete(ctx, uris[start:end], fs.WithSysSkipSoftDelete(true)); err != nil {
			fmt.Printf("cleanup %s batch failed err=%v, fallback to single delete\n", label, err)
			for _, uri := range uris[start:end] {
				if singleErr := fm.Delete(ctx, []*fs.URI{uri}, fs.WithSysSkipSoftDelete(true)); singleErr != nil {
					fmt.Printf("cleanup %s single failed uri=%s err=%v\n", label, uri.String(), singleErr)
				} else {
					fmt.Printf("cleanup %s single ok uri=%s\n", label, uri.String())
				}
			}
			continue
		}

		for _, uri := range uris[start:end] {
			fmt.Printf("cleanup %s batch ok uri=%s\n", label, uri.String())
		}
	}
}
