package manager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	enttask "github.com/cloudreve/Cloudreve/v4/ent/task"
	inventorytypes "github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/searcher"
	searchindexer "github.com/cloudreve/Cloudreve/v4/pkg/searcher/indexer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

type ftsExternalCandidate struct {
	fileModel     *ent.File
	primaryEntity *ent.Entity
	policy        *ent.StoragePolicy
	uri           *fs.URI
}

var (
	loadFTSExternalCandidateForTask = func(m *manager, ctx context.Context, fileID int) (*ftsExternalCandidate, error) {
		return m.loadFTSExternalCandidate(ctx, fileID)
	}
	hasCurrentExternalFTSSidecarForTask = func(m *manager, ctx context.Context, candidate *ftsExternalCandidate) bool {
		return m.hasCurrentExternalFTSSidecar(ctx, candidate)
	}
	findReusableFTSExternalJobForTask = func(ctx context.Context, dep dependency.Dep, fileModel *ent.File, primaryEntity *ent.Entity, cfg *setting.FTSExternalExtractorSetting) (*ent.FTSExternalJob, error) {
		return findReusableFTSExternalJob(ctx, dep, fileModel, primaryEntity, cfg)
	}
	buildFTSFileDocumentForTask = func(m *manager, ctx context.Context, fileID int, opts FTSBuildOptions) (*searcher.SearchFileDocument, *fs.URI, error) {
		return m.buildFTSFileDocumentWithOptions(ctx, fileID, opts)
	}
	hasFTSOCRCandidatesForTask = func(m *manager, ctx context.Context, candidate *ftsExternalCandidate, doc *searcher.SearchFileDocument, uri *fs.URI) bool {
		return m.hasFTSOCRCandidatesAfterLocalBuild(ctx, candidate, doc, uri)
	}
	finalizeExternalIndexedFileForTask = finalizeExternalIndexedFile
	publishFTSExternalRequestForTask   = publishFTSExternalRequest
)

func (m *manager) loadFTSExternalCandidate(ctx context.Context, fileID int) (*ftsExternalCandidate, error) {
	fileModel, err := m.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return nil, err
	}

	primaryEntity := findPrimaryFTSEntity(fileModel)
	if primaryEntity == nil {
		return nil, fmt.Errorf("primary entity not found")
	}

	policy, err := m.storagePolicyFromID(ctx, primaryEntity.StoragePolicyEntities)
	if err != nil {
		return nil, fmt.Errorf("failed to load storage policy for external fts: %w", err)
	}

	uri, err := m.resolveFTSFileURIByModel(ctx, fileModel)
	if err != nil {
		return nil, err
	}

	return &ftsExternalCandidate{
		fileModel:     fileModel,
		primaryEntity: primaryEntity,
		policy:        policy,
		uri:           uri,
	}, nil
}

func (m *manager) hasCurrentExternalFTSSidecar(ctx context.Context, candidate *ftsExternalCandidate) bool {
	if m == nil || candidate == nil || candidate.uri == nil || candidate.primaryEntity == nil || candidate.fileModel == nil {
		return false
	}

	_, manifest, _, _, err := m.loadFTSSidecarManifest(ctx, candidate.uri)
	if err != nil || manifest == nil {
		return false
	}

	var cfg *setting.FTSExternalExtractorSetting
	if m.settings != nil {
		cfg = m.settings.FTSExternalExtractor(ctx)
	}

	return manifest.Provider == ftsSidecarProviderExternal &&
		manifest.EntityID == candidate.primaryEntity.ID &&
		manifest.SnapshotToken == buildFTSExternalSnapshotToken(candidate.fileModel, candidate.primaryEntity, cfg)
}

func (m *manager) hasFTSOCRCandidatesAfterLocalBuild(
	ctx context.Context,
	candidate *ftsExternalCandidate,
	doc *searcher.SearchFileDocument,
	uri *fs.URI,
) bool {
	if m == nil || candidate == nil || candidate.fileModel == nil || candidate.primaryEntity == nil || candidate.policy == nil {
		return false
	}

	var manifest *FTSSidecarManifest
	if uri != nil {
		_, loadedManifest, _, _, err := m.loadFTSSidecarManifest(ctx, uri)
		if err == nil {
			manifest = loadedManifest
		}
	}

	rootText := ""
	if doc != nil {
		rootText = strings.TrimSpace(doc.Content)
	}

	return hasFTSOCRCandidates(candidate.fileModel, fs.NewEntity(candidate.primaryEntity), candidate.policy, rootText, manifest)
}

func upsertFTSDocument(ctx context.Context, fm *manager, uri *fs.URI, doc *searcher.SearchFileDocument) (enttask.Status, error) {
	dep := dependency.FromContext(ctx)
	searchIdx := dep.SearchIndexer(ctx)
	if searchindexer.IsNoopIndexer(searchIdx) {
		return enttask.StatusError, fmt.Errorf("search indexer is unavailable")
	}
	if doc == nil {
		clearFullTextIndexMetadataBestEffort(ctx, fm, uri)
		return enttask.StatusError, fmt.Errorf("search document is nil")
	}

	if err := searchIdx.UpsertFile(ctx, doc); err != nil {
		clearFullTextIndexMetadataBestEffort(ctx, fm, uri)
		return enttask.StatusError, fmt.Errorf("failed to index file %d: %w", doc.FileID, err)
	}

	if uri != nil {
		if err := fm.fs.PatchMetadata(withPublicBypass(ctx, uri), []*fs.URI{uri}, fs.MetadataPatch{
			Key:   dbfs.FullTextIndexKey,
			Value: dbfs.BuildFullTextIndexMetadataValue(fm.hasher, doc.FileID, doc.EntityID),
		}); err != nil {
			return enttask.StatusError, fmt.Errorf("failed to patch metadata: %w", err)
		}
	}

	return enttask.StatusCompleted, nil
}

func (t *FullTextIndexTask) dispatchExternalIfConfigured(
	ctx context.Context,
	fm *manager,
	state *FullTextIndexTaskState,
	item FullTextIndexTaskItem,
) (enttask.Status, bool, error) {
	cfg := fm.settings.FTSExternalExtractor(ctx)
	if cfg == nil {
		return enttask.StatusProcessing, false, nil
	}
	mode := normalizeExternalFTSMode(cfg.Mode)
	if !cfg.Enabled || mode == setting.FTSExternalModeDisabled {
		return enttask.StatusProcessing, false, nil
	}

	candidate, err := loadFTSExternalCandidateForTask(fm, ctx, item.FileID)
	if err != nil {
		return enttask.StatusProcessing, false, nil
	}
	if candidate == nil || candidate.uri == nil || candidate.uri.FileSystem() == constants.FileSystemTrash {
		return enttask.StatusProcessing, false, nil
	}
	if !externalFTSEligible(candidate.fileModel, candidate.primaryEntity, candidate.policy, cfg) {
		return enttask.StatusProcessing, false, nil
	}

	if mode == setting.FTSExternalModePrimary {
		next, err := t.queueExternalExtraction(ctx, fm, state, candidate, "primary", 1, "")
		if err == nil {
			return next, true, nil
		}
		fm.l.Warning("Failed to queue primary external FTS request for file %d, falling back to local extraction: %s", item.FileID, err)
		return enttask.StatusProcessing, false, nil
	}

	doc, uri, err := buildFTSFileDocumentForTask(fm, ctx, item.FileID, FTSBuildOptions{
		ForceTextExtraction:       true,
		ForceAttachmentExtraction: true,
	})
	if err != nil {
		next, queueErr := t.queueExternalExtraction(ctx, fm, state, candidate, "local_build_error", 1, "")
		if queueErr == nil {
			return next, true, nil
		}
		return enttask.StatusError, true, fmt.Errorf("failed to build local fts document for file %d: %v; external fallback failed: %w", item.FileID, err, queueErr)
	}

	if uri == nil {
		uri = candidate.uri
	}

	hasOCRCandidates := cfg.OCREnabled && hasFTSOCRCandidatesForTask(fm, ctx, candidate, doc, uri)
	triggerReason := ""
	qualityReport := ""
	switch mode {
	case setting.FTSExternalModeFallbackOnError:
		if strings.TrimSpace(buildFTSQualityText(doc)) == "" {
			triggerReason = "local_text_empty"
		}
	case setting.FTSExternalModeFallbackOnErrorOrQuality:
		report := evaluateFTSExtractionQuality(doc, cfg)
		if report != nil && !report.Accepted {
			triggerReason = "quality_rejected"
			qualityReport = marshalExternalQualityReport(report)
		}
	}
	if triggerReason == "" && hasOCRCandidates {
		triggerReason = "ocr_candidates_ready"
	}
	if triggerReason != "" {
		next, queueErr := t.queueExternalExtraction(ctx, fm, state, candidate, triggerReason, 1, qualityReport)
		if queueErr == nil {
			return next, true, nil
		}
		fm.l.Warning("Failed to queue external FTS fallback for file %d, using local result: %s", item.FileID, queueErr)
	}

	status, err := upsertFTSDocument(ctx, fm, uri, doc)
	if err != nil {
		return status, true, err
	}

	state.CompleteActive()
	next, err := t.persistAndContinue(state)
	return next, true, err
}

func (t *FullTextIndexTask) queueExternalExtraction(
	ctx context.Context,
	fm *manager,
	state *FullTextIndexTaskState,
	candidate *ftsExternalCandidate,
	triggerReason string,
	attempt int,
	qualityReport string,
) (enttask.Status, error) {
	if candidate == nil || candidate.fileModel == nil || candidate.primaryEntity == nil {
		return enttask.StatusError, fmt.Errorf("invalid external fts candidate")
	}

	if hasCurrentExternalFTSSidecarForTask(fm, ctx, candidate) {
		status, err := fullTextPerformIndexing(ctx, fm, candidate.fileModel.ID)
		if err != nil {
			return status, err
		}
		state.CompleteActive()
		return t.persistAndContinue(state)
	}

	var cfg *setting.FTSExternalExtractorSetting
	if fm.settings != nil {
		cfg = fm.settings.FTSExternalExtractor(ctx)
	}

	reusableJob, err := findReusableFTSExternalJobForTask(ctx, fm.dep, candidate.fileModel, candidate.primaryEntity, cfg)
	if err != nil {
		return enttask.StatusError, err
	}
	if reusableJob != nil {
		switch reusableJob.Status {
		case ftsExternalJobStatusQueued:
			return t.suspendForExternalJob(state, reusableJob.RequestID)
		case ftsExternalJobStatusSuccess:
			status, err := finalizeExternalIndexedFileForTask(ctx, fm, candidate.fileModel.ID, reusableJob)
			if err != nil {
				return status, err
			}
			state.CompleteActive()
			return t.persistAndContinue(state)
		}
	}

	job, err := publishFTSExternalRequestForTask(ctx, fm.dep, candidate.fileModel, candidate.primaryEntity, candidate.policy, fm.settings.FTSExternalExtractor(ctx), triggerReason, attempt, qualityReport)
	if err != nil {
		return enttask.StatusError, err
	}

	return t.suspendForExternalJob(state, job.RequestID)
}

func (t *FullTextIndexTask) suspendForExternalJob(state *FullTextIndexTaskState, requestID string) (enttask.Status, error) {
	state.Phase = fullTextIndexPhaseAwaitExternal
	state.ExternalRequestID = requestID
	state.NodeID = 0
	state.SlaveID = 0
	t.ResumeAfter(10 * time.Second)
	return enttask.StatusSuspending, nil
}

func (t *FullTextIndexTask) awaitExternalExtraction(ctx context.Context, fm *manager, state *FullTextIndexTaskState) (enttask.Status, error) {
	if strings.TrimSpace(state.ExternalRequestID) == "" {
		return enttask.StatusError, fmt.Errorf("missing external request id in full text await phase: %w", queue.CriticalErr)
	}

	job, err := readFTSExternalJobByRequestID(ctx, fm.dep, state.ExternalRequestID)
	if err != nil {
		return enttask.StatusError, fmt.Errorf("failed to query external fts job %s: %w", state.ExternalRequestID, err)
	}

	switch job.Status {
	case ftsExternalJobStatusSuccess:
		item, ok := state.Current()
		if !ok {
			return enttask.StatusError, fmt.Errorf("missing active file in external await phase: %w", queue.CriticalErr)
		}

		status, err := finalizeExternalIndexedFile(ctx, fm, item.FileID, job)
		if err != nil {
			return status, err
		}

		state.CompleteActive()
		return t.persistAndContinue(state)
	case ftsExternalJobStatusError:
		return t.retryOrFallbackExternal(ctx, fm, state, job, "external job reported error")
	default:
		if !job.DeadlineAt.IsZero() && time.Now().After(job.DeadlineAt) {
			return t.retryOrFallbackExternal(ctx, fm, state, job, "external job timed out")
		}
		t.ResumeAfter(10 * time.Second)
		return enttask.StatusSuspending, nil
	}
}

func (t *FullTextIndexTask) retryOrFallbackExternal(
	ctx context.Context,
	fm *manager,
	state *FullTextIndexTaskState,
	job *ent.FTSExternalJob,
	reason string,
) (enttask.Status, error) {
	appendExternalExtractionFailureHistory(t, job, reason)

	cfg := fm.settings.FTSExternalExtractor(ctx)
	item, ok := state.Current()
	if !ok {
		return enttask.StatusError, fmt.Errorf("missing active file in external retry phase: %w", queue.CriticalErr)
	}

	if job != nil && job.Attempt <= cfg.RetryMax {
		candidate, err := fm.loadFTSExternalCandidate(ctx, item.FileID)
		if err == nil && candidate != nil && externalFTSEligible(candidate.fileModel, candidate.primaryEntity, candidate.policy, cfg) {
			next, queueErr := t.queueExternalExtraction(ctx, fm, state, candidate, reason, job.Attempt+1, job.QualityReport)
			if queueErr == nil {
				return next, nil
			}
			fm.l.Warning("Failed to requeue external FTS request for file %d attempt=%d: %s", item.FileID, job.Attempt+1, queueErr)
		}
	}

	markFTSExternalJobLocalFallback(ctx, fm.dep, job, reason)

	status, err := fullTextPerformIndexing(ctx, fm, item.FileID)
	if err != nil {
		return status, fmt.Errorf("%s; local fallback failed: %w", reason, err)
	}

	state.CompleteActive()
	return t.persistAndContinue(state)
}

func appendExternalExtractionFailureHistory(t *FullTextIndexTask, job *ent.FTSExternalJob, reason string) {
	if t == nil || t.DBTask == nil || t.Task == nil {
		return
	}

	message := formatExternalExtractionFailureHistory(job, reason)
	if strings.TrimSpace(message) == "" {
		return
	}

	t.Lock()
	defer t.Unlock()

	if t.Task.PublicState == nil {
		t.Task.PublicState = &inventorytypes.TaskPublicState{}
	}

	t.Task.PublicState.Error = message

	history := t.Task.PublicState.ErrorHistory
	if len(history) > 0 && history[len(history)-1] == message {
		return
	}

	t.Task.PublicState.ErrorHistory = append(history, message)
}

func formatExternalExtractionFailureHistory(job *ent.FTSExternalJob, reason string) string {
	metadata := make([]string, 0, 3)
	if job != nil && strings.TrimSpace(job.RequestID) != "" {
		metadata = append(metadata, "request_id="+strings.TrimSpace(job.RequestID))
	}

	message := ""
	detail := ""
	if job != nil && strings.TrimSpace(job.ErrorPayload) != "" {
		if payload, err := parseFTSExternalErrorPayload(job.ErrorPayload); err == nil && payload != nil {
			if stage := strings.TrimSpace(payload.Stage); stage != "" {
				metadata = append(metadata, "stage="+stage)
			}
			if code := strings.TrimSpace(payload.Code); code != "" {
				metadata = append(metadata, "code="+code)
			}
			message = strings.TrimSpace(payload.Message)
			detail = strings.TrimSpace(payload.Detail)
		} else {
			detail = strings.TrimSpace(job.ErrorPayload)
		}
	}

	prefix := "external fts extraction failed"
	if len(metadata) > 0 {
		prefix = fmt.Sprintf("%s [%s]", prefix, strings.Join(metadata, ", "))
	}

	parts := []string{prefix}
	if message != "" {
		parts = append(parts, message)
	}
	if detail != "" && detail != message {
		parts = append(parts, detail)
	}

	reason = strings.TrimSpace(reason)
	combined := strings.ToLower(strings.Join(parts, " "))
	if reason != "" && !strings.Contains(combined, strings.ToLower(reason)) {
		parts = append(parts, "fallback="+reason)
	}

	return strings.Join(parts, ": ")
}

func finalizeExternalIndexedFile(ctx context.Context, fm *manager, fileID int, job *ent.FTSExternalJob) (enttask.Status, error) {
	dep := dependency.FromContext(ctx)
	searchIdx := dep.SearchIndexer(ctx)
	if searchindexer.IsNoopIndexer(searchIdx) {
		return enttask.StatusError, fmt.Errorf("search indexer is unavailable")
	}
	if job == nil {
		return enttask.StatusError, fmt.Errorf("external fts job is nil")
	}

	uri, err := fm.resolveFTSFileURI(ctx, fileID)
	if err != nil {
		if shouldIgnoreFTSSyncError(err) {
			if err := searchIdx.DeleteByFileIDs(ctx, fileID); err != nil {
				return enttask.StatusError, fmt.Errorf("failed to delete stale index for file %d: %w", fileID, err)
			}
			return enttask.StatusCompleted, nil
		}
		return enttask.StatusError, fmt.Errorf("failed to resolve search uri for file %d: %w", fileID, err)
	}

	if uri != nil && uri.FileSystem() == constants.FileSystemTrash {
		if err := searchIdx.DeleteByFileIDs(ctx, fileID); err != nil {
			return enttask.StatusError, fmt.Errorf("failed to delete index for trashed file %d: %w", fileID, err)
		}
		return enttask.StatusCompleted, nil
	}

	fileModel, err := fm.loadFTSFileModel(ctx, fileID)
	if err != nil {
		return enttask.StatusError, fmt.Errorf("failed to load file model for external finalize: %w", err)
	}

	primaryEntity := findPrimaryFTSEntity(fileModel)
	if primaryEntity == nil {
		return enttask.StatusError, fmt.Errorf("primary entity not found for external finalize")
	}

	result, err := parseFTSExternalResultPayload(job.ResultPayload)
	if err != nil {
		return enttask.StatusError, fmt.Errorf("failed to parse external result payload: %w", err)
	}

	_, manifestPath, err := fm.persistExternalFTSSidecars(ctx, fileModel, uri, fs.NewEntity(primaryEntity), result)
	if err != nil {
		return enttask.StatusError, fmt.Errorf("failed to persist external fts sidecars: %w", err)
	}

	if manifestPath != "" {
		if err := dep.DBClient().FTSExternalJob.UpdateOneID(job.ID).SetManifestPath(manifestPath).Exec(ctx); err != nil {
			fm.l.Warning("Failed to update external fts manifest path for request_id=%s: %s", job.RequestID, err)
		}
	}

	return fullTextPerformIndexing(ctx, fm, fileID)
}
