package dbfs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/samber/lo"
	"golang.org/x/tools/container/intsets"
)

type navigatorFileTarget struct {
	file      *File
	navigator Navigator
}

func (f *DBFS) currentUserHash() string {
	if f == nil || f.user == nil {
		return ""
	}

	return hashid.EncodeUserID(f.hasher, f.user.ID)
}

func (f *DBFS) canManageSharedTrash(target *File) bool {
	if f == nil || f.user == nil || target == nil || target.IsNil() {
		return false
	}

	return strings.TrimSpace(target.Metadata()[MetadataTrashVisibility]) == f.currentUserHash()
}

func (f *DBFS) Create(ctx context.Context, path *fs.URI, fileType types.FileType, opts ...fs.Option) (fs.File, error) {
	o := newDbfsOption()
	for _, opt := range opts {
		o.apply(opt)
	}

	// Get navigator
	navigator, err := f.getNavigator(ctx, path, NavigatorCapabilityCreateFile, NavigatorCapabilityLockFile)
	if err != nil {
		return nil, err
	}

	// Get most recent ancestor
	var ancestor *File
	if o.ancestor != nil {
		ancestor = o.ancestor
	} else {
		ancestor, err = f.getFileByPath(ctx, navigator, path)
		if err != nil && !ent.IsNotFound(err) && !errors.Is(err, fs.ErrPathNotExist) {
			return nil, fmt.Errorf("failed to get ancestor: %w", err)
		}
	}
	if ancestor == nil || ancestor.IsNil() {
		return nil, fs.ErrPathNotExist
	}

	if ancestor.Uri(false).IsSame(path, hashid.EncodeUserID(f.hasher, f.user.ID)) {
		if ancestor.Type() == fileType {
			if o.errOnConflict {
				return ancestor, fs.ErrFileExisted
			}

			// Target file already exist, return it.
			return ancestor, nil
		}

		// File with the same name but different type already exist
		return nil, fs.ErrFileExisted.
			WithError(fmt.Errorf("object with the same name but different type %q already exist", ancestor.Type()))
	}

	if err := ensureCapability(ancestor, NavigatorCapabilityCreateFile); err != nil {
		return nil, err
	}

	if _, ok := ctx.Value(ByPassOwnerCheckCtxKey{}).(bool); !ok && ancestor.Owner().ID != f.user.ID {
		return nil, fs.ErrOwnerOnly
	}

	// Lock ancestor
	lockedPath := ancestor.ResolveOwnerURI(path)
	ls, err := f.acquireByPath(ctx, -1, f.user, false, fs.LockApp(fs.ApplicationCreate),
		&LockByPath{lockedPath, ancestor, fileType, ""})
	defer func() { _ = f.Release(ctx, ls) }()
	if err != nil {
		return nil, err
	}

	// For all ancestors in user's desired path, create folders if not exist
	existedElements := ancestor.Uri(false).Elements()
	desired := path.Elements()
	if (len(desired)-len(existedElements) > 1) && o.noChainedCreation {
		return nil, fs.ErrPathNotExist
	}

	for i := len(existedElements); i < len(desired); i++ {
		// Make sure parent is a folder
		if !ancestor.CanHaveChildren() {
			return nil, fs.ErrNotSupportedAction.WithError(fmt.Errorf("parent must be a valid folder"))
		}

		// Validate object name
		if err := validateFileName(desired[i]); err != nil {
			return nil, fs.ErrIllegalObjectName.WithError(err)
		}

		if i < len(desired)-1 || fileType == types.FileTypeFolder {
			args := &inventory.CreateFolderParameters{
				Owner: ancestor.Model.OwnerID,
				Name:  desired[i],
			}

			// Apply options for last element
			if i == len(desired)-1 {
				if o.Metadata != nil {
					args.Metadata = o.Metadata
				}
				args.IsSymbolic = o.isSymbolicLink
			}

			// Create folder if it is not the last element or the target is a folder
			fc, tx, ctx, err := inventory.WithTx(ctx, f.fileClient)
			if err != nil {
				return nil, serializer.NewError(serializer.CodeDBError, "Failed to start transaction", err)
			}

			newFolder, err := fc.CreateFolder(ctx, ancestor.Model, args)
			if err != nil {
				_ = inventory.Rollback(tx)
				return nil, fmt.Errorf("failed to create folder %q: %w", desired[i], err)
			}

			if err := inventory.Commit(tx); err != nil {
				return nil, serializer.NewError(serializer.CodeDBError, "Failed to commit folder creation", err)
			}

			ancestor = newFile(ancestor, newFolder)
			f.emitFileCreated(ctx, ancestor)
		} else {
			// valide file name
			policy, err := f.getPreferredPolicy(ctx, ancestor)
			if err != nil {
				return nil, err
			}

			if err := validateExtension(desired[i], policy); err != nil {
				return nil, fs.ErrIllegalObjectName.WithError(err)
			}

			if err := validateFileNameRegexp(desired[i], policy); err != nil {
				return nil, fs.ErrIllegalObjectName.WithError(err)
			}

			file, err := f.createFile(ctx, ancestor, desired[i], fileType, o)
			if err != nil {
				return nil, err
			}

			return file, nil
		}
	}

	return ancestor, nil
}

func (f *DBFS) Rename(ctx context.Context, path *fs.URI, newName string) (fs.File, *fs.IndexDiff, error) {
	// Get navigator
	navigator, err := f.getNavigator(ctx, path, NavigatorCapabilityRenameFile, NavigatorCapabilityLockFile)
	if err != nil {
		return nil, nil, err
	}

	// Get target file
	ctx = context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	target, err := f.getFileByPath(ctx, navigator, path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get target file: %w", err)
	}
	oldName := target.Name()

	if err := ensureCapability(target, NavigatorCapabilityRenameFile); err != nil {
		return nil, nil, err
	}

	if _, ok := ctx.Value(ByPassOwnerCheckCtxKey{}).(bool); !ok && target.Owner().ID != f.user.ID {
		return nil, nil, fs.ErrOwnerOnly
	}

	// Root folder cannot be modified
	if target.IsRootFolder() {
		return nil, nil, fs.ErrNotSupportedAction.WithError(fmt.Errorf("cannot modify root folder"))
	}

	// Validate new name
	if err := validateFileName(newName); err != nil {
		return nil, nil, fs.ErrIllegalObjectName.WithError(err)
	}

	if target.Type() == types.FileTypeFile {
		// 仅普通文件需要按存储策略校验扩展名；文件夹改名不应因为 owner group 懒加载缺失而失败。
		policy, err := f.getPreferredPolicy(ctx, target)
		if err != nil {
			return nil, nil, err
		}

		if err := validateExtension(newName, policy); err != nil {
			return nil, nil, fs.ErrIllegalObjectName.WithError(err)
		}

		if err := validateFileNameRegexp(newName, policy); err != nil {
			return nil, nil, fs.ErrIllegalObjectName.WithError(err)
		}
	}

	// Lock target
	ls, err := f.acquireByPath(ctx, -1, f.user, false, fs.LockApp(fs.ApplicationRename),
		&LockByPath{target.Uri(true), target, target.Type(), ""})
	defer func() { _ = f.Release(ctx, ls) }()
	if err != nil {
		return nil, nil, err
	}

	// Rename target
	fc, tx, ctx, err := inventory.WithTx(ctx, f.fileClient)
	if err != nil {
		return nil, nil, serializer.NewError(serializer.CodeDBError, "Failed to start transaction", err)
	}

	indexDiff, err := f.buildRenameIndexDiff(ctx, target, newName)
	if err != nil {
		_ = inventory.Rollback(tx)
		return nil, nil, err
	}

	updated, err := fc.Rename(ctx, target.Model, newName)
	if err != nil {
		_ = inventory.Rollback(tx)
		if ent.IsConstraintError(err) {
			return nil, nil, fs.ErrFileExisted.WithError(err)
		}

		return nil, nil, serializer.NewError(serializer.CodeDBError, "failed to update file", err)
	}

	if target.Type() == types.FileTypeFile && !strings.EqualFold(filepath.Ext(newName), filepath.Ext(oldName)) {
		if err := fc.RemoveMetadata(ctx, target.Model, ThumbDisabledKey); err != nil {
			_ = inventory.Rollback(tx)
			return nil, nil, serializer.NewError(serializer.CodeDBError, "failed to remove disabled thumbnail mark", err)
		}
	}

	if err := inventory.Commit(tx); err != nil {
		return nil, nil, serializer.NewError(serializer.CodeDBError, "Failed to commit rename change", err)
	}

	f.emitFileRenamed(ctx, target, newName)

	originalMetadata := target.Metadata()
	newFile := target.Replace(updated)
	if indexDiff == nil {
		var diff *fs.IndexDiff
		if _, ok := originalMetadata[FullTextIndexKey]; ok {
			diff = &fs.IndexDiff{
				IndexToRename: []fs.IndexDiffRenameDetails{
					{
						Uri:      *newFile.Uri(false),
						FileID:   newFile.ID(),
						EntityID: newFile.PrimaryEntityID(),
					},
				},
			}
			return newFile, diff, nil
		}

		return newFile, nil, nil
	}

	if len(indexDiff.IndexToRename) == 1 {
		indexDiff.IndexToRename[0].Uri = *newFile.Uri(false)
	}

	return newFile, indexDiff, nil
}

func (f *DBFS) SoftDelete(ctx context.Context, path ...*fs.URI) error {
	ae := serializer.NewAggregateError()
	targets := make([]*File, 0, len(path))
	sourceURIByID := make(map[int]*fs.URI, len(path))
	for _, p := range path {
		// Get navigator
		navigator, err := f.getNavigator(ctx, p, NavigatorCapabilitySoftDelete)
		if err != nil {
			ae.Add(p.String(), err)
			continue
		}

		// Get target file
		target, err := f.getFileByPath(ctx, navigator, p)
		if err != nil {
			ae.Add(p.String(), fmt.Errorf("failed to get target file: %w", err))
			continue
		}

		if err := ensureCapability(target, NavigatorCapabilitySoftDelete); err != nil {
			ae.Add(p.String(), err)
			continue
		}

		if _, ok := ctx.Value(ByPassOwnerCheckCtxKey{}).(bool); !ok && target.Owner().ID != f.user.ID {
			ae.Add(p.String(), fs.ErrOwnerOnly.WithError(fmt.Errorf("only file owner can delete file without trash bin")))
			continue
		}

		// Root folder cannot be deleted
		if target.IsRootFolder() {
			ae.Add(p.String(), fs.ErrNotSupportedAction.WithError(fmt.Errorf("cannot delete root folder")))
			continue
		}

		targets = append(targets, target)
		if _, ok := sourceURIByID[target.ID()]; !ok {
			sourceURIByID[target.ID()] = p
		}
	}

	targets = topLevelDBFSTargets(targets, hashid.EncodeUserID(f.hasher, f.user.ID))
	if len(targets) == 0 {
		return ae.Aggregate()
	}
	// Lock all targets
	lockTargets := lo.Map(targets, func(value *File, key int) *LockByPath {
		return &LockByPath{value.Uri(true), value, value.Type(), ""}
	})
	ls, err := f.acquireByPath(ctx, -1, f.user, false, fs.LockApp(fs.ApplicationSoftDelete), lockTargets...)
	defer func() { _ = f.Release(ctx, ls) }()
	if err != nil {
		return err
	}

	// Start transaction to soft-delete files
	fc, tx, ctx, err := inventory.WithTx(ctx, f.fileClient)
	if err != nil {
		return serializer.NewError(serializer.CodeDBError, "Failed to start transaction", err)
	}

	for _, target := range targets {
		// Perform soft-delete
		if err := fc.SoftDelete(ctx, target.Model); err != nil {
			_ = inventory.Rollback(tx)
			return serializer.NewError(serializer.CodeDBError, "failed to soft-delete file", err)
		}

		// Save restore uri into metadata
		owner, ownerErr := f.ensureOwnerWithGroup(ctx, target)
		if ownerErr != nil {
			_ = inventory.Rollback(tx)
			return serializer.NewError(serializer.CodeInternalSetting, "failed to load file owner group", ownerErr)
		}

		sourceURI := sourceURIByID[target.ID()]
		restoreURI := target.Uri(true)
		if sourceURI != nil {
			// Preserve the caller-visible source URI (especially for projected public paths),
			// so restore does not fall back to an inaccessible owner-only `my://<owner>` path.
			restoreURI = sourceURI
		}
		if restoreURI == nil {
			_ = inventory.Rollback(tx)
			return serializer.NewError(serializer.CodeInternalSetting, "failed to resolve restore uri", nil)
		}

		metadataToUpsert := map[string]string{
			MetadataRestoreUri: restoreURI.String(),
			MetadataExpectedCollectTime: strconv.FormatInt(
				time.Now().Add(time.Duration(owner.Edges.Group.Settings.TrashRetention)*time.Second).Unix(),
				10),
		}
		if sourceURI != nil && sourceURI.FileSystem() == constants.FileSystemPublic {
			metadataToUpsert[MetadataTrashVisibility] = f.currentUserHash()
		}

		if err := fc.UpsertMetadata(ctx, target.Model, metadataToUpsert, nil); err != nil {
			_ = inventory.Rollback(tx)
			return serializer.NewError(serializer.CodeDBError, "failed to update metadata", err)
		}
	}

	// Commit transaction
	if err := inventory.Commit(tx); err != nil {
		return serializer.NewError(serializer.CodeDBError, "Failed to commit soft-delete change", err)
	}

	f.emitFileDeleted(ctx, targets...)

	return ae.Aggregate()
}

func (f *DBFS) Delete(ctx context.Context, path []*fs.URI, opts ...fs.Option) ([]fs.Entity, *fs.IndexDiff, error) {
	o := newDbfsOption()
	for _, opt := range opts {
		o.apply(opt)
	}

	var opt *types.EntityProps
	if o.UnlinkOnly {
		opt = &types.EntityProps{
			UnlinkOnly: true,
		}
	}

	ae := serializer.NewAggregateError()
	targetEntries := make([]navigatorFileTarget, 0, len(path))
	ctx = context.WithValue(ctx, inventory.LoadFileEntity{}, true)

	for _, p := range path {
		// Get navigator
		navigator, err := f.getNavigator(ctx, p, NavigatorCapabilityDeleteFile, NavigatorCapabilityLockFile)
		if err != nil {
			ae.Add(p.String(), err)
			continue
		}

		// Get target file
		target, err := f.getFileByPath(ctx, navigator, p)
		if err != nil {
			ae.Add(p.String(), fmt.Errorf("failed to get target file: %w", err))
			continue
		}

		if err := ensureCapability(target, NavigatorCapabilityDeleteFile); err != nil {
			ae.Add(p.String(), err)
			continue
		}

		if _, ok := ctx.Value(ByPassOwnerCheckCtxKey{}).(bool); !o.SysSkipSoftDelete && !ok &&
			target.Owner().ID != f.user.ID && !f.canManageSharedTrash(target) {
			ae.Add(p.String(), fs.ErrOwnerOnly)
			continue
		}

		// Root folder cannot be deleted
		if target.IsRootFolder() {
			ae.Add(p.String(), fs.ErrNotSupportedAction.WithError(fmt.Errorf("cannot delete root folder")))
			continue
		}

		targetEntries = append(targetEntries, navigatorFileTarget{file: target, navigator: navigator})
	}

	targetEntries = topLevelNavigatorFileTargets(targetEntries, hashid.EncodeUserID(f.hasher, f.user.ID))
	fileNavGroup := make(map[Navigator][]*File)
	for _, item := range targetEntries {
		fileNavGroup[item.navigator] = append(fileNavGroup[item.navigator], item.file)
	}
	targets := lo.Flatten(lo.Values(fileNavGroup))
	if len(targets) == 0 {
		return nil, nil, ae.Aggregate()
	}
	// Lock all targets
	lockTargets := lo.Map(targets, func(value *File, key int) *LockByPath {
		return &LockByPath{value.Uri(true), value, value.Type(), ""}
	})
	ls, err := f.acquireByPath(ctx, -1, f.user, false, fs.LockApp(fs.ApplicationDelete), lockTargets...)
	defer func() { _ = f.Release(ctx, ls) }()
	if err != nil {
		return nil, nil, err
	}

	fc, tx, ctx, err := inventory.WithTx(ctx, f.fileClient)
	if err != nil {
		return nil, nil, serializer.NewError(serializer.CodeDBError, "Failed to start transaction", err)
	}

	// Delete targets
	newStaleEntities, storageDiff, indexToDelete, err := f.deleteFiles(ctx, fileNavGroup, fc, opt)
	if err != nil {
		_ = inventory.Rollback(tx)
		return nil, nil, serializer.NewError(serializer.CodeDBError, "failed to delete files", err)
	}

	tx.AppendStorageDiff(storageDiff)
	if err := inventory.CommitWithStorageDiff(ctx, tx, f.l, f.userClient); err != nil {
		return nil, nil, serializer.NewError(serializer.CodeDBError, "Failed to commit delete change", err)
	}
	f.emitFileDeleted(ctx, targets...)
	return newStaleEntities, &fs.IndexDiff{
		IndexToDelete: indexToDelete,
	}, ae.Aggregate()
}

func (f *DBFS) VersionControl(ctx context.Context, path *fs.URI, versionId int, delete bool) (*fs.IndexDiff, error) {
	// Get navigator
	navigator, err := f.getNavigator(ctx, path, NavigatorCapabilityVersionControl)
	if err != nil {
		return nil, err
	}

	// Get target file
	ctx = context.WithValue(ctx, inventory.LoadFileEntity{}, true)
	if !delete {
		ctx = context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	}
	target, err := f.getFileByPath(ctx, navigator, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get target file: %w", err)
	}

	if err := ensureCapability(target, NavigatorCapabilityVersionControl); err != nil {
		return nil, err
	}

	if _, ok := ctx.Value(ByPassOwnerCheckCtxKey{}).(bool); !ok && target.Owner().ID != f.user.ID {
		return nil, fs.ErrOwnerOnly
	}

	// Target must be a file
	if target.Type() != types.FileTypeFile {
		return nil, fs.ErrNotSupportedAction.WithError(fmt.Errorf("target must be a valid file"))
	}

	// Lock file
	ls, err := f.acquireByPath(ctx, -1, f.user, true, fs.LockApp(fs.ApplicationVersionControl),
		&LockByPath{target.Uri(true), target, target.Type(), ""})
	defer func() { _ = f.Release(ctx, ls) }()
	if err != nil {
		return nil, err
	}

	if delete {
		storageDiff, err := f.deleteEntity(ctx, target, versionId)
		if err != nil {
			return nil, err
		}

		if err := f.userClient.ApplyStorageDiff(ctx, storageDiff); err != nil {
			f.l.Error("Failed to apply storage diff after deleting version: %s", err)
		}
		return nil, nil
	} else {
		if err := f.setCurrentVersion(ctx, target, versionId); err != nil {
			return nil, err
		}

		if _, ok := target.Metadata()[FullTextIndexKey]; ok {
			return &fs.IndexDiff{
				IndexToUpdate: []fs.IndexDiffUpdateDetails{
					{
						Uri:      *target.Uri(false),
						FileID:   target.ID(),
						OwnerID:  target.Owner().ID,
						EntityID: versionId,
					},
				},
			}, nil
		}

		return nil, nil
	}
}

func (f *DBFS) Restore(ctx context.Context, path ...*fs.URI) error {
	ae := serializer.NewAggregateError()
	targets := make([]*File, 0, len(path))
	// Restore needs internal metadata (e.g. MetadataTrashVisibility / MetadataRestoreUri)
	// to decide whether shared-trash bypass is allowed.
	ctx = context.WithValue(ctx, inventory.LoadFileMetadata{}, true)
	ctx = context.WithValue(ctx, inventory.LoadFilePublicMetadata{}, true)

	for _, p := range path {
		// Get navigator
		navigator, err := f.getNavigator(ctx, p, NavigatorCapabilityRestore)
		if err != nil {
			ae.Add(p.String(), err)
			continue
		}

		// Get target file
		target, err := f.getFileByPath(ctx, navigator, p)
		if err != nil {
			ae.Add(p.String(), fmt.Errorf("failed to get file: %w", err))
			continue
		}

		targets = append(targets, target)
	}

	targets = topLevelDBFSTargets(targets, hashid.EncodeUserID(f.hasher, f.user.ID))
	if len(targets) == 0 {
		return ae.Aggregate()
	}

	type restoreTask struct {
		target *File
		uris   []*fs.URI
	}
	restoreTasks := lo.FilterMap(targets, func(t *File, key int) (restoreTask, bool) {
		if restoreUri, ok := t.Metadata()[MetadataRestoreUri]; ok {
			srcUrl, err := fs.NewUriFromString(restoreUri)
			if err != nil {
				ae.Add(t.Uri(false).String(), fs.ErrNotSupportedAction.WithError(fmt.Errorf("invalid restore uri: %w", err)))
				return restoreTask{}, false
			}

			return restoreTask{target: t, uris: []*fs.URI{t.Uri(false), srcUrl.DirUri()}}, true
		}

		ae.Add(t.Uri(false).String(), fs.ErrNotSupportedAction.WithError(fmt.Errorf("cannot restore file without required metadata mark")))
		return restoreTask{}, false
	})

	// Copy each file to its original location
	for _, task := range restoreTasks {
		restoreCtx := ctx
		if f.canManageSharedTrash(task.target) {
			restoreCtx = WithBypassOwnerCheck(ctx)
		}

		if _, err := f.MoveOrCopy(restoreCtx, []*fs.URI{task.uris[0]}, task.uris[1], false); err != nil {
			if !ae.Merge(err) {
				ae.Add(task.uris[0].String(), err)
			}
		}
	}

	return ae.Aggregate()

}

func (f *DBFS) MoveOrCopy(ctx context.Context, path []*fs.URI, dst *fs.URI, isCopy bool) (*fs.IndexDiff, error) {
	targetEntries := make([]navigatorFileTarget, 0, len(path))
	sourceCapability := NavigatorCapabilityMoveFile
	if isCopy {
		sourceCapability = NavigatorCapabilityCopyFile
	}

	dstNavigator, err := f.getNavigator(ctx, dst, NavigatorCapabilityLockFile, NavigatorCapabilityCreateFile)
	if err != nil {
		return nil, err
	}

	// Get destination file
	destination, err := f.getFileByPath(ctx, dstNavigator, dst)
	if err != nil {
		return nil, fmt.Errorf("faield to get destination folder: %w", err)
	}
	if err := ensureCapability(destination, NavigatorCapabilityCreateFile); err != nil {
		return nil, err
	}

	if _, ok := ctx.Value(ByPassOwnerCheckCtxKey{}).(bool); !ok && destination.Owner().ID != f.user.ID {
		return nil, fs.ErrOwnerOnly
	}

	// Target must be a folder
	if !destination.CanHaveChildren() {
		return nil, fs.ErrNotSupportedAction.WithError(fmt.Errorf("destination must be a valid folder"))
	}

	ae := serializer.NewAggregateError()
	dstRootPath := destination.Uri(true)
	ctx = context.WithValue(ctx, inventory.LoadFileEntity{}, true)
	ctx = context.WithValue(ctx, inventory.LoadFileMetadata{}, true)

	for _, p := range path {
		pathSourceCapability := sourceCapability
		if !isCopy && p != nil && p.FileSystem() == constants.FileSystemTrash {
			pathSourceCapability = NavigatorCapabilityRestore
		}

		// Get navigator
		navigator, err := f.getNavigator(ctx, p, NavigatorCapabilityLockFile, pathSourceCapability)
		if err != nil {
			ae.Add(p.String(), err)
			continue
		}

		// Check fs capability
		if !canMoveOrCopyTo(p, dst, isCopy) {
			ae.Add(p.String(), fs.ErrNotSupportedAction.WithError(fmt.Errorf("cannot move or copy file form %s to %s", p.String(), dst.String())))
			continue
		}

		// Get target file
		target, err := f.getFileByPath(ctx, navigator, p)
		if err != nil {
			ae.Add(p.String(), fmt.Errorf("failed to get file: %w", err))
			continue
		}
		if err := ensureCapability(target, pathSourceCapability); err != nil {
			ae.Add(p.String(), err)
			continue
		}

		if _, ok := ctx.Value(ByPassOwnerCheckCtxKey{}).(bool); !ok && target.Owner().ID != f.user.ID {
			ae.Add(p.String(), fs.ErrOwnerOnly)
			continue
		}

		// Root folder cannot be moved or copied
		if target.IsRootFolder() {
			ae.Add(p.String(), fs.ErrNotSupportedAction.WithError(fmt.Errorf("cannot move root folder")))
			continue
		}

		// Cannot move or copy folder to its descendant
		if target.Type() == types.FileTypeFolder &&
			dstRootPath.EqualOrIsDescendantOf(target.Uri(true), hashid.EncodeUserID(f.hasher, f.user.ID)) {
			ae.Add(p.String(), fs.ErrNotSupportedAction.WithError(fmt.Errorf("cannot move or copy folder to itself or its descendant")))
			continue
		}
		if !isCopy && target.OwnerID() != destination.OwnerID() {
			// move 只允许在同 owner 树内调整位置，避免 public/my 混合场景下 parent 与 owner 语义错乱。
			ae.Add(p.String(), fs.ErrNotSupportedAction.WithError(fmt.Errorf("cannot move file across different owners")))
			continue
		}

		targetEntries = append(targetEntries, navigatorFileTarget{file: target, navigator: navigator})
	}

	targetEntries = topLevelNavigatorFileTargets(targetEntries, hashid.EncodeUserID(f.hasher, f.user.ID))
	targets := lo.Map(targetEntries, func(item navigatorFileTarget, index int) *File {
		return item.file
	})
	fileNavGroup := make(map[Navigator][]*File)
	if isCopy {
		for _, item := range targetEntries {
			fileNavGroup[item.navigator] = append(fileNavGroup[item.navigator], item.file)
		}
	}

	indexDiff := &fs.IndexDiff{}
	if len(targets) > 0 {
		// Lock all targets
		lockTargets := lo.Map(targets, func(value *File, key int) *LockByPath {
			return &LockByPath{value.Uri(true), value, value.Type(), ""}
		})

		// Lock destination
		dstBase := destination.Uri(true)
		dstLockTargets := lo.Map(targets, func(value *File, key int) *LockByPath {
			return &LockByPath{dstBase.Join(value.Name()), destination, value.Type(), ""}
		})
		allLockTargets := make([]*LockByPath, 0, len(targets)*2)
		if !isCopy {
			// For moving files from trash bin, also lock the dst with restored name.
			dstRestoreTargets := lo.FilterMap(targets, func(value *File, key int) (*LockByPath, bool) {
				if _, ok := value.Metadata()[MetadataRestoreUri]; ok {
					return &LockByPath{dstBase.Join(value.DisplayName()), destination, value.Type(), ""}, true
				}
				return nil, false
			})
			allLockTargets = append(allLockTargets, lockTargets...)
			allLockTargets = append(allLockTargets, dstRestoreTargets...)
		}
		allLockTargets = append(allLockTargets, dstLockTargets...)
		ls, err := f.acquireByPath(ctx, -1, f.user, false, fs.LockApp(fs.ApplicationMoveCopy), allLockTargets...)
		defer func() { _ = f.Release(ctx, ls) }()
		if err != nil {
			return nil, err
		}

		// Start transaction to move files
		fc, tx, ctx, err := inventory.WithTx(ctx, f.fileClient)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to start transaction", err)
		}

		var (
			copiedNewTargetsMap map[int]*ent.File
			storageDiff         inventory.StorageDiff
			indexDiffBatch      *fs.IndexDiff
		)
		if isCopy {
			copiedNewTargetsMap, storageDiff, indexDiffBatch, err = f.copyFiles(ctx, fileNavGroup, destination, fc)
		} else {
			storageDiff, indexDiffBatch, err = f.moveFiles(ctx, targets, destination, fc, dstNavigator)
		}

		if err != nil {
			_ = inventory.Rollback(tx)
			return nil, err
		}

		indexDiff.Merge(indexDiffBatch)
		tx.AppendStorageDiff(storageDiff)
		if err := inventory.CommitWithStorageDiff(ctx, tx, f.l, f.userClient); err != nil {
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to commit move change", err)
		}

		for _, target := range targets {
			if isCopy {
				f.emitFileCreated(ctx, newFile(destination, copiedNewTargetsMap[target.ID()]))
			} else {
				f.emitFileMoved(ctx, target, destination)
			}
		}

		// TODO: after move, dbfs cache should be cleared
	}

	return indexDiff, ae.Aggregate()
}

func topLevelNavigatorFileTargets(targets []navigatorFileTarget, defaultUID string) []navigatorFileTarget {
	filtered := make([]navigatorFileTarget, 0, len(targets))
	for _, candidate := range targets {
		candidateURI := candidate.file.Uri(true)
		skip := false
		for i := 0; i < len(filtered); {
			existingURI := filtered[i].file.Uri(true)
			switch {
			case candidateURI.EqualOrIsDescendantOf(existingURI, defaultUID):
				skip = true
				i = len(filtered)
			case existingURI.EqualOrIsDescendantOf(candidateURI, defaultUID):
				filtered = append(filtered[:i], filtered[i+1:]...)
			default:
				i++
			}
		}

		if !skip {
			filtered = append(filtered, candidate)
		}
	}

	return filtered
}

func topLevelDBFSTargets(targets []*File, defaultUID string) []*File {
	filtered := make([]*File, 0, len(targets))
	for _, candidate := range targets {
		candidateURI := candidate.Uri(true)
		skip := false
		for i := 0; i < len(filtered); {
			existingURI := filtered[i].Uri(true)
			switch {
			case candidateURI.EqualOrIsDescendantOf(existingURI, defaultUID):
				skip = true
				i = len(filtered)
			case existingURI.EqualOrIsDescendantOf(candidateURI, defaultUID):
				filtered = append(filtered[:i], filtered[i+1:]...)
			default:
				i++
			}
		}

		if !skip {
			filtered = append(filtered, candidate)
		}
	}

	return filtered
}

func (f *DBFS) GetFileFromDirectLink(ctx context.Context, dl *ent.DirectLink) (fs.File, error) {
	fileModel, err := dl.Edges.FileOrErr()
	if err != nil {
		return nil, err
	}

	owner, err := fileModel.Edges.OwnerOrErr()
	if err != nil {
		return nil, err
	}

	// File owner must be active
	if owner.Status != user.StatusActive {
		return nil, fs.ErrDirectLinkInvalid.WithError(fmt.Errorf("file owner is not active"))
	}

	file := newFile(nil, fileModel)

	// Traverse to the root file
	baseNavigator := newBaseNavigator(f.fileClient, defaultFilter, f.user, f.hasher, f.settingClient.DBFS(ctx))
	root, err := baseNavigator.findRoot(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("failed to find root file: %w", err)
	}

	if root.Name() != inventory.RootFolderName {
		return nil, serializer.NewError(serializer.CodeNotFound, "direct link not found", err)
	}

	return file, nil
}

func (f *DBFS) TraverseFile(ctx context.Context, fileID int) (fs.File, error) {
	fileModel, err := f.fileClient.GetByID(ctx, fileID)
	if err != nil {
		return nil, err
	}

	if fileModel.OwnerID != f.user.ID && !f.user.Edges.Group.Permissions.Enabled(int(types.GroupPermissionIsAdmin)) {
		return nil, fs.ErrOwnerOnly.WithError(fmt.Errorf("only file owner can traverse file's uri"))
	}

	file := newFile(nil, fileModel)

	// Traverse to the root file
	baseNavigator := newBaseNavigator(f.fileClient, defaultFilter, f.user, f.hasher, f.settingClient.DBFS(ctx))
	root, err := baseNavigator.findRoot(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("failed to find root file: %w", err)
	}

	rootUri := newMyUri()
	if fileModel.OwnerID != f.user.ID {
		rootUri = newMyIDUri(hashid.EncodeUserID(f.hasher, fileModel.OwnerID))
	}

	if root.Name() != inventory.RootFolderName {
		rootUri = newTrashUri(root.Name())
	}

	root.Path[pathIndexRoot] = rootUri
	root.Path[pathIndexUser] = rootUri

	return file, nil
}

func (f *DBFS) deleteEntity(ctx context.Context, target *File, entityId int) (inventory.StorageDiff, error) {
	if target.PrimaryEntityID() == entityId {
		return nil, fs.ErrNotSupportedAction.WithError(fmt.Errorf("cannot delete current version"))
	}

	targetVersion, found := lo.Find(target.Entities(), func(item fs.Entity) bool {
		return item.ID() == entityId
	})
	if !found {
		return nil, fs.ErrEntityNotExist.WithError(fmt.Errorf("version not found"))
	}

	diff, err := f.fileClient.UnlinkEntity(ctx, targetVersion.Model(), target.Model, target.Owner())
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to unlink entity", err)
	}

	if targetVersion.UploadSessionID() != nil {
		err = f.fileClient.RemoveMetadata(ctx, target.Model, MetadataUploadSessionID)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeDBError, "Failed to remove upload session metadata", err)
		}
	}

	f.emitFileModified(ctx, target)
	return diff, nil
}

func (f *DBFS) setCurrentVersion(ctx context.Context, target *File, versionId int) error {
	if target.PrimaryEntityID() == versionId {
		return nil
	}

	targetVersion, found := lo.Find(target.Entities(), func(item fs.Entity) bool {
		return item.ID() == versionId && item.Type() == types.EntityTypeVersion && item.UploadSessionID() == nil
	})
	if !found {
		return fs.ErrEntityNotExist.WithError(fmt.Errorf("version not found"))
	}

	fc, tx, ctx, err := inventory.WithTx(ctx, f.fileClient)
	if err != nil {
		return serializer.NewError(serializer.CodeDBError, "Failed to start transaction", err)
	}

	if err := fc.SetPrimaryEntity(ctx, target.Model, targetVersion.Model()); err != nil {
		_ = inventory.Rollback(tx)
		return serializer.NewError(serializer.CodeDBError, "Failed to set primary entity", err)
	}

	// Cap thumbnail entities
	diff, err := fc.CapEntities(ctx, target.Model, target.Owner(), 0, types.EntityTypeThumbnail)
	if err != nil {
		_ = inventory.Rollback(tx)
		return serializer.NewError(serializer.CodeDBError, "Failed to cap thumbnail entities", err)
	}

	tx.AppendStorageDiff(diff)
	if err := inventory.CommitWithStorageDiff(ctx, tx, f.l, f.userClient); err != nil {
		return serializer.NewError(serializer.CodeDBError, "Failed to commit set current version", err)
	}

	f.emitFileModified(ctx, target)
	return nil
}

func (f *DBFS) deleteFiles(ctx context.Context, targets map[Navigator][]*File, fc inventory.FileClient, opt *types.EntityProps) ([]fs.Entity, inventory.StorageDiff, []int, error) {
	if f.user.Edges.Group == nil {
		return nil, nil, nil, fmt.Errorf("user group not loaded")
	}
	allStaleEntities := make([]fs.Entity, 0, len(targets))
	storageDiff := make(inventory.StorageDiff)
	indexToDelete := make([]int, 0)
	for n, files := range targets {
		// Let navigator use tx
		reset, err := n.FollowTx(ctx)
		if err != nil {
			return nil, nil, nil, err
		}

		defer reset()

		// List all files to be deleted
		toBeDeletedFiles := make([]*File, 0, len(files))
		if err := n.Walk(ctx, files, intsets.MaxInt, intsets.MaxInt, func(targets []*File, level int) error {
			toBeDeletedFiles = append(toBeDeletedFiles, targets...)
			indexToDelete = append(indexToDelete, lo.Map(targets, func(item *File, index int) int {
				return item.ID()
			})...)
			return nil
		}); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to walk files: %w", err)
		}

		// Delete files
		staleEntities, diff, err := fc.Delete(ctx, lo.Map(toBeDeletedFiles, func(item *File, index int) *ent.File {
			return item.Model
		}), opt)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to delete files: %w", err)
		}
		storageDiff.Merge(diff)
		allStaleEntities = append(allStaleEntities, lo.Map(staleEntities, func(item *ent.Entity, index int) fs.Entity {
			return fs.NewEntity(item)
		})...)
	}

	return allStaleEntities, storageDiff, indexToDelete, nil
}

func (f *DBFS) copyFiles(ctx context.Context, targets map[Navigator][]*File, destination *File, fc inventory.FileClient) (map[int]*ent.File, inventory.StorageDiff, *fs.IndexDiff, error) {
	if f.user.Edges.Group == nil {
		return nil, nil, nil, fmt.Errorf("user group not loaded")
	}
	limit := max(f.user.Edges.Group.Settings.MaxWalkedFiles, 1)
	capacity, err := f.Capacity(ctx, destination.Owner())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("copy files: failed to destination owner capacity: %w", err)
	}

	dstAncestors := lo.Map(destination.AncestorsChain(), func(item *File, index int) *ent.File {
		return item.Model
	})

	// newTargetsMap is the map of between new target files in first layer, and its src file ID.
	newTargetsMap := make(map[int]*ent.File)
	allCopiedTargetsMap := make(map[int]*ent.File)
	copiedUserURI := make(map[int]*fs.URI)
	storageDiff := make(inventory.StorageDiff)
	indexToCopy := make([]fs.IndexDiffCopyDetails, 0)
	var diff inventory.StorageDiff
	for n, files := range targets {
		initialDstMap := make(map[int][]*ent.File)
		for _, file := range files {
			initialDstMap[file.Model.FileChildren] = dstAncestors
		}

		firstLayer := true
		// Let navigator use tx
		reset, err := n.FollowTx(ctx)
		if err != nil {
			return nil, nil, nil, err
		}

		defer reset()

		if err := n.Walk(ctx, files, limit, intsets.MaxInt, func(targets []*File, level int) error {
			// check capacity for each file
			sizeTotal := int64(0)
			for _, file := range targets {
				sizeTotal += file.SizeUsed()
			}

			if err := f.validateUserCapacityRaw(ctx, sizeTotal, capacity); err != nil {
				return fs.ErrInsufficientCapacity
			}

			limit -= len(targets)
			initialDstMap, diff, err = fc.Copy(ctx, &inventory.CopyParameter{
				Files: lo.Map(targets, func(item *File, index int) *ent.File {
					return item.Model
				}),
				ExcludedMetadataKeys: []string{
					FullTextIndexKey,
					FTSSidecarManifestKey,
					FTSSidecarEntityIDKey,
				},
				DstMap: initialDstMap,
			})
			if err != nil {
				if ent.IsConstraintError(err) {
					return fs.ErrFileExisted.WithError(err)
				}

				return serializer.NewError(serializer.CodeDBError, "Failed to copy files", err)
			}

			storageDiff.Merge(diff)
			if firstLayer {
				for k, v := range initialDstMap {
					newTargetsMap[k] = v[0]
				}
			}
			for k, v := range initialDstMap {
				if len(v) == 0 || v[0] == nil {
					continue
				}
				allCopiedTargetsMap[k] = v[0]
			}

			for _, file := range targets {
				copiedFile := allCopiedTargetsMap[file.ID()]
				if copiedFile == nil {
					continue
				}

				parentURI, ok := copiedUserURI[file.Model.FileChildren]
				if !ok || parentURI == nil {
					parentURI = destination.Uri(false)
				}
				copiedURI := parentURI.Join(copiedFile.Name)
				copiedUserURI[file.ID()] = copiedURI

				if _, ok := file.Metadata()[FullTextIndexKey]; ok {
					indexToCopy = append(indexToCopy, fs.IndexDiffCopyDetails{
						OriginalFileID: file.ID(),
						FileID:         copiedFile.ID,
						Uri:            *copiedURI,
						EntityID:       copiedFile.PrimaryEntity,
						OwnerID:        destination.OwnerID(),
					})
				}
			}

			capacity.Used += sizeTotal
			firstLayer = false

			return nil
		}); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to walk files: %w", err)
		}
	}

	var indexDiff *fs.IndexDiff
	if len(indexToCopy) > 0 {
		indexDiff = &fs.IndexDiff{
			IndexToCopy: indexToCopy,
		}
	}

	return newTargetsMap, storageDiff, indexDiff, nil
}

func (f *DBFS) moveFiles(ctx context.Context, targets []*File, destination *File, fc inventory.FileClient, _ Navigator) (inventory.StorageDiff, *fs.IndexDiff, error) {
	indexDiff, err := f.buildMoveIndexDiff(ctx, targets, destination)
	if err != nil {
		return nil, nil, err
	}

	models := lo.Map(targets, func(value *File, key int) *ent.File {
		return value.Model
	})

	// Change targets' parent
	if err := fc.SetParent(ctx, models, destination.Model); err != nil {
		if ent.IsConstraintError(err) {
			return nil, nil, fs.ErrFileExisted.WithError(err)
		}

		return nil, nil, serializer.NewError(serializer.CodeDBError, "Failed to move file", err)
	}

	var (
		storageDiff inventory.StorageDiff
	)

	// For files moved out from trash bin
	for _, file := range targets {
		if _, ok := file.Metadata()[MetadataRestoreUri]; !ok {
			continue
		}

		// renaming it to its original name
		if _, err := fc.Rename(ctx, file.Model, file.DisplayName()); err != nil {
			if ent.IsConstraintError(err) {
				return nil, nil, fs.ErrFileExisted.WithError(err)
			}

			return storageDiff, nil, serializer.NewError(serializer.CodeDBError, "Failed to rename file from trash bin to its original name", err)
		}

		// Remove trash bin metadata
		if err := fc.RemoveMetadata(ctx, file.Model,
			MetadataRestoreUri, MetadataExpectedCollectTime, MetadataTrashVisibility); err != nil {
			return storageDiff, nil, serializer.NewError(serializer.CodeDBError, "Failed to remove trash related metadata", err)
		}
	}

	return storageDiff, indexDiff, nil
}

func (f *DBFS) buildMoveIndexDiff(ctx context.Context, targets []*File, destination *File) (*fs.IndexDiff, error) {
	if len(targets) == 0 || destination == nil {
		return nil, nil
	}

	defaultUID := hashid.EncodeUserID(f.hasher, f.user.ID)
	diff := &fs.IndexDiff{}
	for _, target := range targets {
		if target == nil {
			continue
		}

		oldRoot := target.Uri(false)
		newBase := destination.Uri(false)
		if oldRoot == nil || newBase == nil {
			continue
		}

		newRoot := newBase.Join(target.Name())
		if _, ok := target.Metadata()[MetadataRestoreUri]; ok {
			newRoot = newBase.Join(target.DisplayName())
		}

		if err := f.Walk(withHiddenPublicRootAccess(ctx, target), target.Uri(true), -1, func(file fs.File, level int) error {
			dbFile, ok := file.(*File)
			if !ok || dbFile == nil {
				return nil
			}

			if _, ok := dbFile.Metadata()[FullTextIndexKey]; !ok {
				return nil
			}

			oldURI := dbFile.Uri(false)
			if oldURI == nil {
				return nil
			}

			nextURI := newRoot
			if !oldURI.IsSame(oldRoot, defaultUID) {
				nextURI = newRoot.Rebase(oldURI, oldRoot)
			}

			diff.IndexToRename = append(diff.IndexToRename, fs.IndexDiffRenameDetails{
				Uri:      *nextURI,
				FileID:   dbFile.ID(),
				EntityID: dbFile.PrimaryEntityID(),
			})
			return nil
		}); err != nil {
			return nil, err
		}
	}

	if len(diff.IndexToRename) == 0 {
		return nil, nil
	}

	return diff, nil
}

func (f *DBFS) buildRenameIndexDiff(ctx context.Context, target *File, newName string) (*fs.IndexDiff, error) {
	if target == nil || target.Parent == nil {
		return nil, nil
	}

	oldRoot := target.Uri(false)
	parentURI := target.Parent.Uri(false)
	if oldRoot == nil || parentURI == nil {
		return nil, nil
	}

	newRoot := parentURI.Join(newName)
	return f.buildRebasedIndexDiff(ctx, []*File{target}, func(current *File, oldRootURI *fs.URI) *fs.URI {
		oldURI := current.Uri(false)
		if oldURI == nil {
			return nil
		}
		if oldURI.IsSame(oldRootURI, hashid.EncodeUserID(f.hasher, f.user.ID)) {
			return newRoot
		}
		return newRoot.Rebase(oldURI, oldRootURI)
	})
}

func (f *DBFS) buildRebasedIndexDiff(ctx context.Context, targets []*File, rebase func(current *File, oldRoot *fs.URI) *fs.URI) (*fs.IndexDiff, error) {
	if len(targets) == 0 || rebase == nil {
		return nil, nil
	}

	diff := &fs.IndexDiff{}
	for _, target := range targets {
		if target == nil {
			continue
		}

		oldRoot := target.Uri(false)
		if oldRoot == nil {
			continue
		}

		if err := f.Walk(withHiddenPublicRootAccess(ctx, target), target.Uri(true), -1, func(file fs.File, level int) error {
			dbFile, ok := file.(*File)
			if !ok || dbFile == nil {
				return nil
			}

			if _, ok := dbFile.Metadata()[FullTextIndexKey]; !ok {
				return nil
			}

			nextURI := rebase(dbFile, oldRoot)
			if nextURI == nil {
				return nil
			}

			diff.IndexToRename = append(diff.IndexToRename, fs.IndexDiffRenameDetails{
				Uri:      *nextURI,
				FileID:   dbFile.ID(),
				EntityID: dbFile.PrimaryEntityID(),
			})
			return nil
		}); err != nil {
			return nil, err
		}
	}

	if len(diff.IndexToRename) == 0 {
		return nil, nil
	}

	return diff, nil
}
