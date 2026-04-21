package admin

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	entfile "github.com/cloudreve/Cloudreve/v4/ent/file"
	entuser "github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/cloudreve/Cloudreve/v4/pkg/thumb"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/cloudreve/Cloudreve/v4/service/user"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func init() {
	gob.Register(map[string]interface{}{})
	gob.Register(map[string]string{})
}

// NoParamService 无需参数的服务
type NoParamService struct {
}

// BatchSettingChangeService 设定批量更改服务
type BatchSettingChangeService struct {
	Options []SettingChangeService `json:"options"`
}

// SettingChangeService  设定更改服务
type SettingChangeService struct {
	Key   string `json:"key" binding:"required"`
	Value string `json:"value"`
}

// Change 批量更改站点设定
func (service *BatchSettingChangeService) Change() serializer.Response {
	//cacheClean := make([]string, 0, len(service.Options))
	//tx := model.DB.Begin()
	//
	//for _, setting := range service.Options {
	//
	//	if err := tx.Model(&model.Setting{}).Where("name = ?", setting.Key).Update("value", setting.Value).Error; err != nil {
	//		cache.Deletes(cacheClean, "setting_")
	//		tx.Rollback()
	//		return serializer.ErrDeprecated(serializer.CodeUpdateSetting, "Setting "+setting.Key+" failed to update", err)
	//	}
	//
	//	cacheClean = append(cacheClean, setting.Key)
	//}
	//
	//if err := tx.Commit().Error; err != nil {
	//	return serializer.DBErrDeprecated("Failed to update setting", err)
	//}
	//
	//cache.Deletes(cacheClean, "setting_")

	return serializer.Response{}
}

const (
	SummaryRangeDays = 12
	MetricCacheKey   = "admin_summary_v5"
	metricErrMsg     = "Failed to generate metrics summary"
	topUploadUsers   = 10
)

type (
	SummaryService struct {
		Generate        bool `form:"generate"`
		UploadRangeDays int  `form:"upload_range_days"`
	}
	SummaryParamCtx struct{}
)

// Summary 获取站点统计概况
func (s *SummaryService) Summary(c *gin.Context) (*HomepageSummary, error) {
	dep := dependency.FromContext(c)
	kv := dep.KV()
	uploadRangeDays := normalizeTopUploadRangeDays(s.UploadRangeDays)
	cacheKey := summaryCacheKey(uploadRangeDays)
	res := &HomepageSummary{
		Version: &Version{
			Version: constants.BackendVersion,
			Pro:     constants.IsProBool,
			Commit:  constants.LastCommit,
		},
		SiteURls: lo.Map(dep.SettingProvider().AllSiteURLs(c), func(item *url.URL, index int) string {
			return item.String()
		}),
	}

	if !s.Generate {
		if summary, ok := kv.Get(cacheKey); ok {
			summaryCasted := summary.(MetricsSummary)
			res.MetricsSummary = &summaryCasted
			return res, nil
		}

		return res, nil
	}

	summary := &MetricsSummary{
		Files:       make([]int, SummaryRangeDays),
		Users:       make([]int, SummaryRangeDays),
		Shares:      make([]int, SummaryRangeDays),
		Dates:       make([]time.Time, SummaryRangeDays),
		GeneratedAt: time.Now(),
	}

	fileClient := dep.FileClient()
	userClient := dep.UserClient()
	shareClient := dep.ShareClient()

	toRound := time.Now()
	timeBase := time.Date(toRound.Year(), toRound.Month(), toRound.Day()+1, 0, 0, 0, 0, toRound.Location())
	for day := range summary.Files {
		start := timeBase.Add(-time.Duration(SummaryRangeDays-day) * time.Hour * 24)
		end := timeBase.Add(-time.Duration(SummaryRangeDays-day-1) * time.Hour * 24)
		summary.Dates[day] = start
		fileTotal, err := fileClient.CountByTimeRange(c, &start, &end)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeDBError, metricErrMsg, nil)
		}
		userTotal, err := userClient.CountByTimeRange(c, &start, &end)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeDBError, metricErrMsg, nil)
		}
		shareTotal, err := shareClient.CountByTimeRange(c, &start, &end)
		if err != nil {
			return nil, serializer.NewError(serializer.CodeDBError, metricErrMsg, nil)
		}
		summary.Files[day] = fileTotal
		summary.Users[day] = userTotal
		summary.Shares[day] = shareTotal
	}

	var err error
	summary.FileTotal, err = fileClient.CountByTimeRange(c, nil, nil)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, metricErrMsg, nil)
	}
	summary.UserTotal, err = userClient.CountByTimeRange(c, nil, nil)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, metricErrMsg, nil)
	}
	summary.ShareTotal, err = shareClient.CountByTimeRange(c, nil, nil)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, metricErrMsg, nil)
	}
	summary.EntitiesTotal, err = fileClient.CountEntityByTimeRange(c, nil, nil)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, metricErrMsg, nil)
	}
	summary.TopUploadUsers, err = s.topUploadUsers(c, dep, uploadRangeDays)
	if err != nil {
		dep.Logger().Warning("Failed to load top upload users for admin summary: %s", err)
		summary.TopUploadUsers = []UserUploadStat{}
	}

	_ = kv.Set(cacheKey, *summary, 86400)
	res.MetricsSummary = summary

	return res, nil
}

func summaryCacheKey(uploadRangeDays int) string {
	return fmt.Sprintf("%s:%d", MetricCacheKey, normalizeTopUploadRangeDays(uploadRangeDays))
}

func normalizeTopUploadRangeDays(days int) int {
	switch days {
	case 7, 30:
		return days
	default:
		return 0
	}
}

func (s *SummaryService) topUploadUsers(ctx context.Context, dep dependency.Dep, uploadRangeDays int) ([]UserUploadStat, error) {
	query := dep.DBClient().File.Query().Where(
		entfile.TypeEQ(int(types.FileTypeFile)),
		entfile.OwnerIDNEQ(constants.PublicSystemOwnerID),
	)
	if uploadRangeDays > 0 {
		query = query.Where(entfile.CreatedAtGTE(time.Now().AddDate(0, 0, -uploadRangeDays)))
	}

	grouped := make([]struct {
		OwnerID   int `json:"owner_id"`
		FileCount int `json:"count"`
	}, 0, topUploadUsers)
	err := query.GroupBy(entfile.FieldOwnerID).Aggregate(ent.Count()).Scan(ctx, &grouped)
	if err != nil {
		return nil, err
	}
	sort.Slice(grouped, func(i, j int) bool {
		if grouped[i].FileCount == grouped[j].FileCount {
			return grouped[i].OwnerID < grouped[j].OwnerID
		}
		return grouped[i].FileCount > grouped[j].FileCount
	})
	if len(grouped) > topUploadUsers {
		grouped = grouped[:topUploadUsers]
	}
	if len(grouped) == 0 {
		return []UserUploadStat{}, nil
	}

	userIDs := make([]int, 0, len(grouped))
	for _, item := range grouped {
		userIDs = append(userIDs, item.OwnerID)
	}

	users, err := dep.DBClient().User.Query().Where(entuser.IDIn(userIDs...)).All(ctx)
	if err != nil {
		return nil, err
	}

	userByID := make(map[int]*ent.User, len(users))
	for _, u := range users {
		userByID[u.ID] = u
	}

	result := make([]UserUploadStat, 0, len(grouped))
	for _, item := range grouped {
		displayName := fmt.Sprintf("用户 #%d", item.OwnerID)
		if u, ok := userByID[item.OwnerID]; ok {
			displayName = userUploadDisplayName(u)
		}

		result = append(result, UserUploadStat{
			UserID:      item.OwnerID,
			DisplayName: displayName,
			FileCount:   item.FileCount,
		})
	}

	return result, nil
}

func userUploadDisplayName(u *ent.User) string {
	if u == nil {
		return ""
	}

	if nick := strings.TrimSpace(u.Nick); nick != "" {
		return nick
	}
	if u.Username != nil {
		if username := strings.TrimSpace(*u.Username); username != "" {
			return username
		}
	}
	if email := strings.TrimSpace(u.Email); email != "" {
		return email
	}

	return fmt.Sprintf("用户 #%d", u.ID)
}

// ThumbGeneratorTestService 缩略图生成测试服务
type (
	ThumbGeneratorTestService struct {
		Name       string `json:"name" binding:"required"`
		Executable string `json:"executable" binding:"required"`
	}
	ThumbGeneratorTestParamCtx struct{}
)

// Test 通过获取生成器版本来测试
func (s *ThumbGeneratorTestService) Test(c *gin.Context) (string, error) {
	version, err := thumb.TestGenerator(c, s.Name, s.Executable)
	if err != nil {
		return "", serializer.NewError(serializer.CodeParamErr, "Failed to invoke generator: "+err.Error(), err)
	}

	return version, nil
}

type (
	GetSettingService struct {
		Keys []string `json:"keys" binding:"required"`
	}
	GetSettingParamCtx struct{}
)

type (
	GetOIDCRuntimeStateService  struct{}
	GetOIDCRuntimeStateParamCtx struct{}
)

func (s *GetSettingService) GetSetting(c *gin.Context) (map[string]string, error) {
	dep := dependency.FromContext(c)
	res, err := dep.SettingClient().Gets(c, lo.Filter(s.Keys, func(item string, index int) bool {
		_, ok := inventory.RedactedSettings[strings.ToLower(item)]
		return !ok
	}))
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to get settings", err)
	}

	return res, nil
}

func (s *GetOIDCRuntimeStateService) Get(c *gin.Context) (*setting.OIDCRuntimeState, error) {
	dep := dependency.FromContext(c)
	state := user.GetOIDCRuntimeState(c, dep)
	if state == nil {
		return &setting.OIDCRuntimeState{
			Status: setting.OIDCRuntimeStatusDisabled,
			Source: setting.OIDCRuntimeConfigSourceLocal,
		}, nil
	}
	return state, nil
}

type (
	SetSettingService struct {
		Settings map[string]string `json:"settings" binding:"required"`
	}
	SetSettingParamCtx   struct{}
	SettingPreProcessor  func(ctx context.Context, settings map[string]string) error
	SettingPostProcessor func(ctx context.Context, settings map[string]string) error
)

var (
	preprocessors = map[string]SettingPreProcessor{
		"siteURL":      siteUrlPreProcessor,
		"mime_mapping": mimeMappingPreProcessor,
		"secret_key":   secretKeyPreProcessor,
	}
	postprocessors = map[string]SettingPostProcessor{
		"mime_mapping":                            mimeMappingPostProcessor,
		"media_meta_exif":                         mediaMetaPostProcessor,
		"media_meta_music":                        mediaMetaPostProcessor,
		"media_meta_ffprobe":                      mediaMetaPostProcessor,
		"smtpUser":                                emailPostProcessor,
		"smtpPass":                                emailPostProcessor,
		"smtpHost":                                emailPostProcessor,
		"smtpPort":                                emailPostProcessor,
		"smtpEncryption":                          emailPostProcessor,
		"smtpFrom":                                emailPostProcessor,
		"replyTo":                                 emailPostProcessor,
		"fromName":                                emailPostProcessor,
		"fromAdress":                              emailPostProcessor,
		"queue_media_meta_worker_num":             contentProcessingQueuePostProcessor,
		"queue_media_meta_max_execution":          contentProcessingQueuePostProcessor,
		"queue_media_meta_backoff_factor":         contentProcessingQueuePostProcessor,
		"queue_media_meta_backoff_max_duration":   contentProcessingQueuePostProcessor,
		"queue_media_meta_max_retry":              contentProcessingQueuePostProcessor,
		"queue_media_meta_retry_delay":            contentProcessingQueuePostProcessor,
		"queue_content_processing_worker_num":     contentProcessingQueuePostProcessor,
		"queue_content_processing_max_execution":  contentProcessingQueuePostProcessor,
		"queue_content_processing_backoff_factor": contentProcessingQueuePostProcessor,
		"queue_content_processing_backoff_max_duration": contentProcessingQueuePostProcessor,
		"queue_content_processing_max_retry":            contentProcessingQueuePostProcessor,
		"queue_content_processing_retry_delay":          contentProcessingQueuePostProcessor,
		"queue_thumb_worker_num":                        thumbQueuePostProcessor,
		"queue_thumb_max_execution":                     thumbQueuePostProcessor,
		"queue_thumb_backoff_factor":                    thumbQueuePostProcessor,
		"queue_thumb_backoff_max_duration":              thumbQueuePostProcessor,
		"queue_thumb_max_retry":                         thumbQueuePostProcessor,
		"queue_thumb_retry_delay":                       thumbQueuePostProcessor,
		"queue_recycle_worker_num":                      entityRecycleQueuePostProcessor,
		"queue_recycle_max_execution":                   entityRecycleQueuePostProcessor,
		"queue_recycle_backoff_factor":                  entityRecycleQueuePostProcessor,
		"queue_recycle_backoff_max_duration":            entityRecycleQueuePostProcessor,
		"queue_recycle_max_retry":                       entityRecycleQueuePostProcessor,
		"queue_recycle_retry_delay":                     entityRecycleQueuePostProcessor,
		"queue_io_intense_worker_num":                   ioIntenseQueuePostProcessor,
		"queue_io_intense_max_execution":                ioIntenseQueuePostProcessor,
		"queue_io_intense_backoff_factor":               ioIntenseQueuePostProcessor,
		"queue_io_intense_backoff_max_duration":         ioIntenseQueuePostProcessor,
		"queue_io_intense_max_retry":                    ioIntenseQueuePostProcessor,
		"queue_io_intense_retry_delay":                  ioIntenseQueuePostProcessor,
		"queue_remote_download_worker_num":              remoteDownloadQueuePostProcessor,
		"queue_remote_download_max_execution":           remoteDownloadQueuePostProcessor,
		"queue_remote_download_backoff_factor":          remoteDownloadQueuePostProcessor,
		"queue_remote_download_backoff_max_duration":    remoteDownloadQueuePostProcessor,
		"queue_remote_download_max_retry":               remoteDownloadQueuePostProcessor,
		"queue_remote_download_retry_delay":             remoteDownloadQueuePostProcessor,
		"secret_key":                                    secretKeyPostProcessor,
		"fts_enabled":                                   meilisearchPostProcessor,
		"fts_sync_folders":                              meilisearchPostProcessor,
		"fts_index_type":                                meilisearchPostProcessor,
		"fts_chunk_size":                                meilisearchPostProcessor,
		"fts_meilisearch_embed_config":                  meilisearchPostProcessor,
		"fts_meilisearch_endpoint":                      meilisearchPostProcessor,
		"fts_meilisearch_api_key":                       meilisearchPostProcessor,
		"fts_meilisearch_embed_enabled":                 meilisearchPostProcessor,
		"fts_meilisearch_page_size":                     meilisearchPostProcessor,
		"fts_elasticsearch_endpoint":                    meilisearchPostProcessor,
		"fts_elasticsearch_cloud_id":                    meilisearchPostProcessor,
		"fts_elasticsearch_api_key":                     meilisearchPostProcessor,
		"fts_elasticsearch_username":                    meilisearchPostProcessor,
		"fts_elasticsearch_password":                    meilisearchPostProcessor,
		"fts_elasticsearch_index":                       meilisearchPostProcessor,
		"fts_elasticsearch_page_size":                   meilisearchPostProcessor,
		"fts_elasticsearch_skip_tls_verify":             meilisearchPostProcessor,
		"fts_tika_endpoint":                             tikaPostProcessor,
		"fts_tika_document_enabled":                     tikaPostProcessor,
		"fts_tika_document_exts":                        tikaPostProcessor,
		"fts_tika_archive_enabled":                      tikaPostProcessor,
		"fts_tika_archive_exts":                         tikaPostProcessor,
		"fts_tika_max_file_size":                        tikaPostProcessor,
		"fts_tika_sidecar_enabled":                      tikaPostProcessor,
		"fts_tika_sidecar_text_enabled":                 tikaPostProcessor,
		"fts_tika_sidecar_assets_enabled":               tikaPostProcessor,
		"fts_tika_extract_inline_images":                tikaPostProcessor,
		"fts_external_enabled":                          externalFTSPostProcessor,
		"fts_external_use_global_kafka":                 externalFTSPostProcessor,
		"fts_external_kafka_brokers":                    externalFTSPostProcessor,
		"fts_external_kafka_security_protocol":          externalFTSPostProcessor,
		"fts_external_kafka_sasl_mechanism":             externalFTSPostProcessor,
		"fts_external_kafka_username":                   externalFTSPostProcessor,
		"fts_external_kafka_password":                   externalFTSPostProcessor,
		"fts_external_kafka_tls_skip_verify":            externalFTSPostProcessor,
		"fts_external_kafka_process_topic":              externalFTSPostProcessor,
		"fts_external_kafka_result_topic":               externalFTSPostProcessor,
		"fts_external_kafka_error_topic":                externalFTSPostProcessor,
		"fts_external_kafka_consumer_group":             externalFTSPostProcessor,
	}
	reloadFTSExternalKafka = manager.ReloadFTSExternalKafka
)

func (s *SetSettingService) SetSetting(c *gin.Context) (map[string]string, error) {
	dep := dependency.FromContext(c)
	kv := dep.KV()
	settingClient := dep.SettingClient()

	// Preprocess settings
	allPreprocessors := make(map[string]SettingPreProcessor)
	allPostprocessors := make(map[string]SettingPostProcessor)
	for k, _ := range s.Settings {
		if preprocessor, ok := preprocessors[k]; ok {
			key := processorKey(preprocessor)
			if _, ok := allPreprocessors[key]; !ok {
				allPreprocessors[key] = preprocessor
			}
		}

		if postprocessor, ok := postprocessors[k]; ok {
			key := processorKey(postprocessor)
			if _, ok := allPostprocessors[key]; !ok {
				allPostprocessors[key] = postprocessor
			}
		}
	}

	// Execute all preprocessors
	for _, preprocessor := range allPreprocessors {
		if err := preprocessor(c, s.Settings); err != nil {
			return nil, serializer.NewError(serializer.CodeParamErr, "Failed to validate settings", err)
		}
	}

	// Save to db
	sc, tx, ctx, err := inventory.WithTx(c, settingClient)
	if err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to create transaction", err)
	}

	if err := sc.Set(ctx, s.Settings); err != nil {
		_ = inventory.Rollback(tx)
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to save settings", err)
	}

	if err := inventory.Commit(tx); err != nil {
		return nil, serializer.NewError(serializer.CodeDBError, "Failed to commit transaction", err)
	}

	// Clean cache
	if err := kv.Delete(setting.KvSettingPrefix, lo.Keys(s.Settings)...); err != nil {
		return nil, serializer.NewError(serializer.CodeInternalSetting, "Failed to clear cache", err)
	}

	// Execute post preprocessors
	for _, postprocessor := range allPostprocessors {
		if err := postprocessor(ctx, s.Settings); err != nil {
			return nil, serializer.NewError(serializer.CodeParamErr, "Failed to post process settings", err)
		}
	}

	return s.Settings, nil
}

func siteUrlPreProcessor(ctx context.Context, settings map[string]string) error {
	siteURL := settings["siteURL"]
	urls := strings.Split(siteURL, ",")
	for index, u := range urls {
		urlParsed, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("Failed to parse siteURL %q: %w", u, err)
		}

		urls[index] = urlParsed.String()
	}
	settings["siteURL"] = strings.Join(urls, ",")
	return nil
}

func secretKeyPreProcessor(ctx context.Context, settings map[string]string) error {
	settings["secret_key"] = util.RandStringRunesCrypto(256)
	return nil
}

func mimeMappingPreProcessor(ctx context.Context, settings map[string]string) error {
	raw := strings.TrimSpace(settings["mime_mapping"])
	var mapping map[string]string
	if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
		return serializer.NewError(serializer.CodeParamErr, "Invalid mime mapping", err)
	}

	normalized := make(map[string]string, len(mapping))
	originalKeys := make(map[string]string, len(mapping))
	for rawExt, rawContentType := range mapping {
		ext := strings.ToLower(strings.TrimSpace(rawExt))
		if ext == "" {
			return serializer.NewError(serializer.CodeParamErr, "Invalid mime mapping", fmt.Errorf("empty extension key"))
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}

		contentType := strings.TrimSpace(rawContentType)
		if contentType == "" {
			return serializer.NewError(serializer.CodeParamErr, "Invalid mime mapping", fmt.Errorf("empty MIME type for %s", rawExt))
		}
		contentType = normalizeMimeMappingContentType(contentType)

		if previous, ok := originalKeys[ext]; ok && previous != rawExt {
			return serializer.NewError(serializer.CodeParamErr, "Invalid mime mapping", fmt.Errorf("duplicate extension after normalization: %s and %s", previous, rawExt))
		}

		originalKeys[ext] = rawExt
		normalized[ext] = contentType
	}

	keys := lo.Keys(normalized)
	sort.Strings(keys)
	ordered := make(map[string]string, len(keys))
	for _, key := range keys {
		ordered[key] = normalized[key]
	}

	encoded, err := json.Marshal(ordered)
	if err != nil {
		return serializer.NewError(serializer.CodeParamErr, "Invalid mime mapping", err)
	}

	settings["mime_mapping"] = string(encoded)
	return nil
}

func mimeMappingPostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	reloadCtx := context.WithValue(ctx, dependency.ReloadCtx{}, true)
	dep.MimeDetector(reloadCtx)
	dep.TextExtractor(reloadCtx)

	return nil
}

func processorKey(fn any) string {
	value := reflect.ValueOf(fn)
	if !value.IsValid() || value.IsNil() {
		return ""
	}

	return runtime.FuncForPC(value.Pointer()).Name()
}

func normalizeMimeMappingContentType(contentType string) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return contentType
	}

	normalized := mime.FormatMediaType(mediaType, params)
	if normalized != "" {
		return normalized
	}

	return mediaType
}

func mediaMetaPostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.MediaMetaExtractor(context.WithValue(ctx, dependency.ReloadCtx{}, true))
	return nil
}

func emailPostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.EmailClient(context.WithValue(ctx, dependency.ReloadCtx{}, true))
	return nil
}

func contentProcessingQueuePostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.ContentProcessingQueue(context.WithValue(ctx, dependency.ReloadCtx{}, true)).Start()
	return nil
}

func ioIntenseQueuePostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.IoIntenseQueue(context.WithValue(ctx, dependency.ReloadCtx{}, true)).Start()
	return nil
}

func remoteDownloadQueuePostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.RemoteDownloadQueue(context.WithValue(ctx, dependency.ReloadCtx{}, true)).Start()
	return nil
}

func entityRecycleQueuePostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.EntityRecycleQueue(context.WithValue(ctx, dependency.ReloadCtx{}, true)).Start()
	return nil
}

func thumbQueuePostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.ThumbQueue(context.WithValue(ctx, dependency.ReloadCtx{}, true)).Start()
	return nil
}

func secretKeyPostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.KV().Delete(manager.EntityUrlCacheKeyPrefix)
	settings["secret_key"] = ""
	return nil
}

func meilisearchPostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.SearchIndexer(context.WithValue(ctx, dependency.ReloadCtx{}, true))
	return nil
}

func tikaPostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	dep.TextExtractor(context.WithValue(ctx, dependency.ReloadCtx{}, true))
	return nil
}

func externalFTSPostProcessor(ctx context.Context, settings map[string]string) error {
	dep := dependency.FromContext(ctx)
	return reloadFTSExternalKafka(ctx, dep)
}
