package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/golang-jwt/jwt/v5"
	_ "github.com/lib/pq"
)

var (
	baseURL        = env("CLOUDREVE_URL", "http://127.0.0.1:5212")
	esURL          = env("CLOUDREVE_ES_URL", "http://127.0.0.1:19200")
	pgDSN          = env("CLOUDREVE_PG_DSN", "postgres://cloudreve:cloudreve@127.0.0.1:15432/cloudreve?sslmode=disable")
	minioContainer = env("CLOUDREVE_MINIO_CONTAINER", "cloudreve-dev-minio")
	minioBucketDir = env("CLOUDREVE_MINIO_BUCKET_DIR", "/bitnami/minio/data/cloudreve")
)

type apiResponse struct {
	Code          int             `json:"code"`
	Msg           string          `json:"msg"`
	Data          json.RawMessage `json:"data"`
	CorrelationID string          `json:"correlation_id"`
}

type fileResponse struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Type          int               `json:"type"`
	Path          string            `json:"path"`
	Size          int64             `json:"size"`
	PrimaryEntity string            `json:"primary_entity"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type listResponse struct {
	Files []fileResponse `json:"files"`
}

type uploadSessionResponse struct {
	SessionID string `json:"session_id"`
	ChunkSize int64  `json:"chunk_size"`
	Uri       string `json:"uri"`
}

type sidecarResponse struct {
	Version     int             `json:"version"`
	FileID      int             `json:"file_id"`
	EntityID    int             `json:"entity_id"`
	SourcePath  string          `json:"source_path"`
	ExtractedAt string          `json:"extracted_at"`
	Objects     []sidecarObject `json:"objects"`
}

type sidecarObject struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id,omitempty"`
	Depth      int    `json:"depth,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	MimeType   string `json:"mime_type"`
	Size       int64  `json:"size"`
	URI        string `json:"uri,omitempty"`
	PreviewURI string `json:"preview_uri,omitempty"`
	URL        string `json:"url"`
	PreviewURL string `json:"preview_url,omitempty"`
}

type taskSummary struct {
	ID           int             `json:"id"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	Error        string          `json:"error,omitempty"`
	ErrorHistory json.RawMessage `json:"error_history,omitempty"`
	StatePreview string          `json:"state_preview,omitempty"`
}

type esDocSummary struct {
	ID                          string         `json:"id"`
	FileID                      any            `json:"file_id"`
	EntityID                    any            `json:"entity_id"`
	OwnerID                     any            `json:"owner_id"`
	FileName                    string         `json:"file_name"`
	PublicURI                   string         `json:"public_uri"`
	OwnerURI                    string         `json:"owner_uri"`
	TreePath                    string         `json:"tree_path"`
	SearchPaths                 []string       `json:"search_paths"`
	SearchURIs                  []string       `json:"search_uris"`
	ContentContainsKeyword      bool           `json:"content_contains_keyword"`
	AttachmentContainsKeyword   bool           `json:"attachment_contains_keyword"`
	AttachmentSourceUsesSidecar bool           `json:"attachment_source_uses_sidecar"`
	HasLatestVersion            bool           `json:"has_latest_version"`
	Keys                        []string       `json:"keys"`
	Source                      map[string]any `json:"source,omitempty"`
}

type stepResult struct {
	ID         string `json:"id"`
	Item       string `json:"item"`
	Status     string `json:"status"`
	Evidence   string `json:"evidence,omitempty"`
	Failure    string `json:"failure,omitempty"`
	FixPath    string `json:"fix_path,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

type report struct {
	StartedAt      time.Time                 `json:"started_at"`
	CompletedAt    time.Time                 `json:"completed_at"`
	BaseURL        string                    `json:"base_url"`
	ESURL          string                    `json:"es_url"`
	PGDSN          string                    `json:"pg_dsn"`
	MinioContainer string                    `json:"minio_container"`
	MinioBucketDir string                    `json:"minio_bucket_dir"`
	EvidenceDir    string                    `json:"evidence_dir"`
	PublicRootURI  string                    `json:"public_root_uri"`
	TextFileURI    string                    `json:"text_file_uri"`
	ZipFileURI     string                    `json:"zip_file_uri"`
	TextKeyword    string                    `json:"text_keyword"`
	ZipKeyword     string                    `json:"zip_keyword"`
	SettingsBefore map[string]string         `json:"settings_before,omitempty"`
	SettingsAfter  map[string]string         `json:"settings_after,omitempty"`
	TextSidecar    *sidecarResponse          `json:"text_sidecar,omitempty"`
	ZipSidecar     *sidecarResponse          `json:"zip_sidecar,omitempty"`
	MinioObjects   map[string]bool           `json:"minio_objects,omitempty"`
	ESDocs         []esDocSummary            `json:"es_docs,omitempty"`
	Tasks          []taskSummary             `json:"tasks,omitempty"`
	Downloads      map[string]downloadRecord `json:"downloads,omitempty"`
	Steps          []stepResult              `json:"steps"`
}

type downloadRecord struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	Contains    bool   `json:"contains_expected_text"`
	FinalURL    string `json:"final_url,omitempty"`
}

type client struct {
	http  *http.Client
	db    *sql.DB
	token string
}

func main() {
	ctx := context.Background()
	db, err := sql.Open("postgres", pgDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	token, err := issueLocalAdminToken(ctx, db)
	if err != nil {
		log.Fatal(err)
	}

	now := time.Now()
	suffix := now.Format("20060102150405")
	evidenceDir := path.Join(".tmp", "public-sidecar-minio-es-"+suffix)
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		log.Fatal(err)
	}

	r := &report{
		StartedAt:      now,
		BaseURL:        baseURL,
		ESURL:          esURL,
		PGDSN:          pgDSN,
		MinioContainer: minioContainer,
		MinioBucketDir: minioBucketDir,
		EvidenceDir:    evidenceDir,
		TextKeyword:    "CRSidecarPublicText" + suffix,
		ZipKeyword:     "CRSidecarAttachment" + suffix,
		MinioObjects:   map[string]bool{},
		Downloads:      map[string]downloadRecord{},
	}

	c := &client{
		http: &http.Client{
			Timeout: 90 * time.Second,
		},
		db:    db,
		token: token,
	}

	run := func(id, item string, fn func() (string, error)) {
		if err := runStep(r, id, item, fn); err != nil {
			finishReport(ctx, c, r)
			log.Fatal(err)
		}
	}

	root := "cloudreve://public/sidecar-minio-es-" + suffix
	publicDir := root + "/cases"
	textName := "public-sidecar-text-" + suffix + ".txt"
	zipName := "public-sidecar-archive-" + suffix + ".zip"
	textURI := publicDir + "/" + textName
	zipURI := publicDir + "/" + zipName
	r.PublicRootURI = root
	r.TextFileURI = textURI
	r.ZipFileURI = zipURI

	run("PSC-00", "Cloudreve 健康检查", func() (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v4/site/ping", nil)
		if err != nil {
			return "", err
		}
		res, err := c.http.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return "", fmt.Errorf("site ping status=%d body=%s", res.StatusCode, string(raw))
		}
		return strings.TrimSpace(string(raw)), nil
	})

	run("PSC-01", "读取当前 FTS/Tika/sidecar 设置", func() (string, error) {
		settings, err := c.settings(ctx, []string{
			"fts_enabled",
			"fts_index_type",
			"fts_extractor_type",
			"fts_elasticsearch_endpoint",
			"fts_tika_endpoint",
			"fts_tika_sidecar_enabled",
			"fts_tika_sidecar_text_enabled",
			"fts_tika_sidecar_assets_enabled",
		})
		if err != nil {
			return "", err
		}
		r.SettingsBefore = settings
		raw, _ := json.Marshal(settings)
		return string(raw), nil
	})

	run("PSC-02", "通过管理 API 开启 FTS/Tika sidecar", func() (string, error) {
		patch := map[string]string{
			"fts_enabled":                     "1",
			"fts_index_type":                  "elasticsearch",
			"fts_extractor_type":              "tika",
			"fts_elasticsearch_endpoint":      esURL,
			"fts_tika_endpoint":               env("CLOUDREVE_TIKA_URL", "http://127.0.0.1:19998"),
			"fts_tika_sidecar_enabled":        "1",
			"fts_tika_sidecar_text_enabled":   "1",
			"fts_tika_sidecar_assets_enabled": "1",
		}
		if err := c.patchSettings(ctx, patch); err != nil {
			return "", err
		}
		settings, err := c.settings(ctx, []string{
			"fts_enabled",
			"fts_elasticsearch_endpoint",
			"fts_tika_endpoint",
			"fts_tika_sidecar_enabled",
			"fts_tika_sidecar_text_enabled",
			"fts_tika_sidecar_assets_enabled",
		})
		if err != nil {
			return "", err
		}
		r.SettingsAfter = settings
		if settings["fts_tika_sidecar_enabled"] != "1" {
			return "", fmt.Errorf("sidecar setting not enabled: %#v", settings)
		}
		raw, _ := json.Marshal(settings)
		return string(raw), nil
	})

	run("PSC-03", "创建公共测试目录", func() (string, error) {
		if _, err := c.create(ctx, root, "folder"); err != nil {
			return "", err
		}
		if _, err := c.create(ctx, publicDir, "folder"); err != nil {
			return "", err
		}
		return publicDir, nil
	})

	taskBaseline, err := c.maxTaskID(ctx)
	if err != nil {
		finishReport(ctx, c, r)
		log.Fatal(err)
	}

	run("PSC-04", "公共目录上传文本文件", func() (string, error) {
		content := "public text sidecar body " + r.TextKeyword + "\n"
		file, err := c.uploadBytes(ctx, textURI, []byte(content), "text/plain; charset=utf-8")
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(file)
		return string(raw), nil
	})

	run("PSC-05", "公共目录上传含附件文本的 zip 文件", func() (string, error) {
		rawZip, err := buildZip(map[string]string{
			"nested/note.txt": "zip nested sidecar body " + r.ZipKeyword + "\n",
		})
		if err != nil {
			return "", err
		}
		file, err := c.uploadBytes(ctx, zipURI, rawZip, "application/zip")
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(file)
		return string(raw), nil
	})

	run("PSC-06", "等待文本文件 ES 文档完整", func() (string, error) {
		doc, err := c.waitESDoc(ctx, r.TextKeyword, textName, 180*time.Second)
		if err != nil {
			return "", err
		}
		if !doc.ContentContainsKeyword || doc.PublicURI == "" || doc.OwnerURI == "" || doc.TreePath == "" || len(doc.SearchPaths) == 0 || len(doc.SearchURIs) == 0 || !doc.HasLatestVersion {
			return "", fmt.Errorf("text ES document incomplete: %+v", doc)
		}
		raw, _ := json.Marshal(doc)
		return string(raw), nil
	})

	run("PSC-07", "等待 zip 附件 ES 文档完整", func() (string, error) {
		doc, err := c.waitESDoc(ctx, r.ZipKeyword, zipName, 180*time.Second)
		if err != nil {
			return "", err
		}
		if !doc.AttachmentContainsKeyword || !doc.AttachmentSourceUsesSidecar || doc.PublicURI == "" || doc.OwnerURI == "" || doc.TreePath == "" || len(doc.SearchPaths) == 0 || len(doc.SearchURIs) == 0 || !doc.HasLatestVersion {
			return "", fmt.Errorf("zip ES document incomplete: %+v", doc)
		}
		raw, _ := json.Marshal(doc)
		return string(raw), nil
	})

	run("PSC-08", "等待文本文件 sidecar manifest", func() (string, error) {
		sidecar, err := c.waitSidecar(ctx, textURI, 180*time.Second)
		if err != nil {
			return "", err
		}
		r.TextSidecar = sidecar
		if _, ok := findObject(sidecar.Objects, func(item sidecarObject) bool { return item.Kind == "text" && item.ID == "content.txt" }); !ok {
			return "", fmt.Errorf("text sidecar missing content.txt: %+v", sidecar.Objects)
		}
		raw, _ := json.Marshal(sidecar)
		return string(raw), nil
	})

	run("PSC-09", "等待 zip 文件 sidecar manifest 与附件预览对象", func() (string, error) {
		sidecar, err := c.waitSidecar(ctx, zipURI, 180*time.Second)
		if err != nil {
			return "", err
		}
		r.ZipSidecar = sidecar
		attachment, ok := findObject(sidecar.Objects, func(item sidecarObject) bool {
			return item.Kind != "text" && item.PreviewURL != ""
		})
		if !ok {
			return "", fmt.Errorf("zip sidecar missing previewable attachment: %+v", sidecar.Objects)
		}
		raw, _ := json.Marshal(attachment)
		return string(raw), nil
	})

	run("PSC-10", "核验 sidecar 对象已落到 MinIO", func() (string, error) {
		checked := map[string]bool{}
		for _, sidecar := range []*sidecarResponse{r.TextSidecar, r.ZipSidecar} {
			if sidecar == nil {
				continue
			}
			for _, item := range sidecar.Objects {
				if item.Path == "" {
					continue
				}
				ok := minioObjectExists(item.Path)
				r.MinioObjects[item.Path] = ok
				checked[item.Path] = ok
				if !ok {
					return "", fmt.Errorf("minio object missing for sidecar path %s", item.Path)
				}
			}
			manifestPath := sidecarManifestPath(sidecar)
			if manifestPath != "" {
				ok := minioObjectExists(manifestPath)
				r.MinioObjects[manifestPath] = ok
				checked[manifestPath] = ok
				if !ok {
					return "", fmt.Errorf("minio manifest missing for path %s", manifestPath)
				}
			}
		}
		raw, _ := json.Marshal(checked)
		return string(raw), nil
	})

	run("PSC-11", "预览文本 sidecar 正文内容", func() (string, error) {
		item, ok := findObject(r.TextSidecar.Objects, func(item sidecarObject) bool {
			return item.Kind == "text" && item.URL != ""
		})
		if !ok {
			return "", fmt.Errorf("text sidecar has no signed content url")
		}
		rec, err := c.downloadURL(ctx, item.URL, r.TextKeyword)
		if err != nil {
			return "", err
		}
		r.Downloads["text_content"] = rec
		if !rec.Contains {
			return "", fmt.Errorf("text sidecar content mismatch: %+v", rec)
		}
		raw, _ := json.Marshal(rec)
		return string(raw), nil
	})

	run("PSC-12", "预览 zip 附件抽取正文", func() (string, error) {
		item, ok := findObject(r.ZipSidecar.Objects, func(item sidecarObject) bool {
			return item.Kind != "text" && item.PreviewURL != ""
		})
		if !ok {
			return "", fmt.Errorf("zip sidecar has no attachment preview url")
		}
		rec, err := c.downloadURL(ctx, item.PreviewURL, r.ZipKeyword)
		if err != nil {
			return "", err
		}
		r.Downloads["zip_attachment_preview"] = rec
		if !rec.Contains {
			return "", fmt.Errorf("zip attachment preview mismatch: %+v", rec)
		}
		raw, _ := json.Marshal(rec)
		return string(raw), nil
	})

	run("PSC-13", "下载 zip 附件对象内容", func() (string, error) {
		item, ok := findObject(r.ZipSidecar.Objects, func(item sidecarObject) bool {
			return item.Kind != "text" && item.URL != "" && strings.HasSuffix(item.Name, ".txt")
		})
		if !ok {
			return "", fmt.Errorf("zip sidecar has no downloadable text attachment")
		}
		rec, err := c.downloadURL(ctx, item.URL, r.ZipKeyword)
		if err != nil {
			return "", err
		}
		r.Downloads["zip_attachment_object"] = rec
		if !rec.Contains {
			return "", fmt.Errorf("zip attachment object mismatch: %+v", rec)
		}
		raw, _ := json.Marshal(rec)
		return string(raw), nil
	})

	run("PSC-14", "任务列表无本轮非预期失败", func() (string, error) {
		tasks, err := c.waitTasksSettled(ctx, suffix, taskBaseline, 180*time.Second)
		if err != nil {
			return "", err
		}
		r.Tasks = tasks
		for _, task := range tasks {
			if task.Status == "error" || task.Status == "canceled" {
				return "", fmt.Errorf("task %d failed: %+v", task.ID, task)
			}
		}
		raw, _ := json.Marshal(tasks)
		return string(raw), nil
	})

	finishReport(ctx, c, r)
	fmt.Printf("Report: %s\n", path.Join(r.EvidenceDir, "report.json"))
}

func runStep(r *report, id, item string, fn func() (string, error)) error {
	evidence, err := fn()
	if err != nil {
		r.Steps = append(r.Steps, stepResult{
			ID:         id,
			Item:       item,
			Status:     "Fail",
			Failure:    err.Error(),
			Conclusion: "阻断，需要修复后回归",
		})
		fmt.Printf("[FAIL] %s %s :: %s\n", id, item, err)
		return err
	}
	r.Steps = append(r.Steps, stepResult{
		ID:         id,
		Item:       item,
		Status:     "Pass",
		Evidence:   evidence,
		Conclusion: "通过",
	})
	fmt.Printf("[PASS] %s %s\n", id, item)
	return nil
}

func (c *client) apiJSON(ctx context.Context, method, apiPath string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+apiPath, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%s %s status=%d body=%s", method, apiPath, res.StatusCode, string(raw))
	}
	resp := apiResponse{}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("decode response: %w body=%s", err, string(raw))
	}
	if resp.Code != 0 {
		return fmt.Errorf("%s %s code=%d msg=%s correlation=%s", method, apiPath, resp.Code, resp.Msg, resp.CorrelationID)
	}
	if out != nil && len(resp.Data) > 0 && string(resp.Data) != "null" {
		if err := json.Unmarshal(resp.Data, out); err != nil {
			return fmt.Errorf("decode data: %w data=%s", err, string(resp.Data))
		}
	}
	return nil
}

func (c *client) settings(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	err := c.apiJSON(ctx, http.MethodPost, "/api/v4/admin/settings", map[string]any{"keys": keys}, &out)
	return out, err
}

func (c *client) patchSettings(ctx context.Context, settings map[string]string) error {
	return c.apiJSON(ctx, http.MethodPatch, "/api/v4/admin/settings", map[string]any{"settings": settings}, nil)
}

func (c *client) create(ctx context.Context, uri, typ string) (fileResponse, error) {
	var out fileResponse
	err := c.apiJSON(ctx, http.MethodPost, "/api/v4/file/create", map[string]any{
		"uri":             uri,
		"type":            typ,
		"err_on_conflict": true,
	}, &out)
	return out, err
}

func (c *client) uploadBytes(ctx context.Context, uri string, content []byte, mimeType string) (fileResponse, error) {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	var session uploadSessionResponse
	if err := c.apiJSON(ctx, http.MethodPut, "/api/v4/file/upload", map[string]any{
		"uri":           uri,
		"size":          len(content),
		"mime_type":     mimeType,
		"last_modified": time.Now().UnixMilli(),
	}, &session); err != nil {
		return fileResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v4/file/upload/"+session.SessionID+"/0", bytes.NewReader(content))
	if err != nil {
		return fileResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(content)))
	res, err := c.http.Do(req)
	if err != nil {
		return fileResponse{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	resp := apiResponse{}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fileResponse{}, fmt.Errorf("decode upload response: %w body=%s", err, string(raw))
	}
	if resp.Code != 0 {
		return fileResponse{}, fmt.Errorf("upload code=%d msg=%s correlation=%s", resp.Code, resp.Msg, resp.CorrelationID)
	}
	return c.findByName(ctx, parentURI(uri), fileName(uri))
}

func (c *client) findByName(ctx context.Context, dir, name string) (fileResponse, error) {
	var list listResponse
	if err := c.apiJSON(ctx, http.MethodGet, "/api/v4/file?uri="+url.QueryEscape(dir)+"&page=0&page_size=200", nil, &list); err != nil {
		return fileResponse{}, err
	}
	for _, file := range list.Files {
		if file.Name == name {
			return file, nil
		}
	}
	return fileResponse{}, fmt.Errorf("file %s not found under %s", name, dir)
}

func (c *client) getSidecar(ctx context.Context, uri string) (*sidecarResponse, error) {
	var out sidecarResponse
	err := c.apiJSON(ctx, http.MethodGet, "/api/v4/file/fulltext/sidecar?uri="+url.QueryEscape(uri), nil, &out)
	return &out, err
}

func (c *client) waitSidecar(ctx context.Context, uri string, timeout time.Duration) (*sidecarResponse, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		sidecar, err := c.getSidecar(ctx, uri)
		if err == nil && sidecar != nil && len(sidecar.Objects) > 0 {
			return sidecar, nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("sidecar not ready for %s: %w", uri, lastErr)
}

func (c *client) downloadURL(ctx context.Context, rawURL, expected string) (downloadRecord, error) {
	target := rawURL
	if strings.HasPrefix(target, "/") {
		target = baseURL + target
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return downloadRecord{}, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return downloadRecord{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return downloadRecord{}, err
	}
	rec := downloadRecord{
		URL:         rawURL,
		Status:      res.StatusCode,
		ContentType: res.Header.Get("Content-Type"),
		Size:        len(body),
		Contains:    strings.Contains(string(body), expected),
		FinalURL:    res.Request.URL.String(),
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return rec, fmt.Errorf("download status=%d body=%s", res.StatusCode, string(body))
	}
	return rec, nil
}

func (c *client) maxTaskID(ctx context.Context) (int, error) {
	var id int
	err := c.db.QueryRowContext(ctx, `select coalesce(max(id), 0) from tasks`).Scan(&id)
	return id, err
}

func (c *client) queryTasks(ctx context.Context, suffix string, afterID int) ([]taskSummary, error) {
	rows, err := c.db.QueryContext(ctx, `
select id,type,status,
       coalesce(public_state->>'error',''),
       coalesce(public_state->'error_history','[]'::jsonb),
       left(private_state, 320)
from tasks
where id > $1
  and (private_state like '%' || $2 || '%' or public_state::text like '%' || $2 || '%')
order by id`, afterID, suffix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []taskSummary
	for rows.Next() {
		var task taskSummary
		if err := rows.Scan(&task.ID, &task.Type, &task.Status, &task.Error, &task.ErrorHistory, &task.StatePreview); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (c *client) waitTasksSettled(ctx context.Context, suffix string, afterID int, timeout time.Duration) ([]taskSummary, error) {
	deadline := time.Now().Add(timeout)
	var last []taskSummary
	for time.Now().Before(deadline) {
		tasks, err := c.queryTasks(ctx, suffix, afterID)
		if err == nil {
			last = tasks
			if len(tasks) > 0 && tasksSettled(tasks) {
				return tasks, nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return last, fmt.Errorf("tasks not settled: %+v", last)
}

func tasksSettled(tasks []taskSummary) bool {
	for _, task := range tasks {
		switch task.Status {
		case "queued", "processing", "suspending":
			return false
		}
	}
	return true
}

func (c *client) queryESDocs(ctx context.Context, keyword string) ([]esDocSummary, error) {
	body := map[string]any{
		"size": 20,
		"query": map[string]any{
			"bool": map[string]any{
				"should": []any{
					map[string]any{"match_phrase": map[string]any{"content": keyword}},
					map[string]any{"match_phrase": map[string]any{"attachments.content": keyword}},
					map[string]any{"match_phrase": map[string]any{"file_name": keyword}},
				},
				"minimum_should_match": 1,
			},
		},
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, esURL+"/cloudreve_files/_search", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	respRaw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("es status=%d body=%s", res.StatusCode, string(respRaw))
	}
	var resp struct {
		Hits struct {
			Hits []struct {
				ID     string         `json:"_id"`
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		return nil, err
	}
	out := make([]esDocSummary, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		src := hit.Source
		keys := make([]string, 0, len(src))
		for key := range src {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = append(out, esDocSummary{
			ID:                          hit.ID,
			FileID:                      src["file_id"],
			EntityID:                    src["entity_id"],
			OwnerID:                     src["owner_id"],
			FileName:                    str(src["file_name"]),
			PublicURI:                   str(src["public_uri"]),
			OwnerURI:                    str(src["owner_uri"]),
			TreePath:                    str(src["tree_path"]),
			SearchPaths:                 strSlice(src["search_paths"]),
			SearchURIs:                  strSlice(src["search_uris"]),
			ContentContainsKeyword:      strings.Contains(str(src["content"]), keyword),
			AttachmentContainsKeyword:   attachmentsContain(src["attachments"], "content", keyword),
			AttachmentSourceUsesSidecar: attachmentsSourceUsesSidecar(src["attachments"]),
			HasLatestVersion:            src["latest_version"] != nil,
			Keys:                        keys,
			Source:                      src,
		})
	}
	return out, nil
}

func (c *client) waitESDoc(ctx context.Context, keyword, fileName string, timeout time.Duration) (esDocSummary, error) {
	deadline := time.Now().Add(timeout)
	var last []esDocSummary
	for time.Now().Before(deadline) {
		docs, err := c.queryESDocs(ctx, keyword)
		if err == nil {
			last = docs
			for _, doc := range docs {
				if doc.FileName == fileName {
					return doc, nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return esDocSummary{}, fmt.Errorf("ES document not found for file=%s keyword=%s last=%+v", fileName, keyword, last)
}

func finishReport(ctx context.Context, c *client, r *report) {
	r.CompletedAt = time.Now()
	docs := make([]esDocSummary, 0)
	if textDocs, err := c.queryESDocs(ctx, r.TextKeyword); err == nil {
		docs = append(docs, textDocs...)
	}
	if zipDocs, err := c.queryESDocs(ctx, r.ZipKeyword); err == nil {
		docs = append(docs, zipDocs...)
	}
	r.ESDocs = docs
	if len(r.Tasks) == 0 {
		if tasks, err := c.queryTasks(ctx, r.StartedAt.Format("20060102150405"), 0); err == nil {
			r.Tasks = tasks
		}
	}
	writeReport(r)
}

func writeReport(r *report) {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		log.Printf("failed to marshal report: %s", err)
		return
	}
	if err := os.WriteFile(path.Join(r.EvidenceDir, "report.json"), raw, 0o644); err != nil {
		log.Printf("failed to write report: %s", err)
	}
}

func issueLocalAdminToken(ctx context.Context, db *sql.DB) (string, error) {
	values := map[string]string{}
	rows, err := db.QueryContext(ctx, `select name, value from settings where name in ('secret_key','hash_id_salt','siteID')`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return "", err
		}
		values[name] = value
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	encoder, err := hashid.New(values["hash_id_salt"])
	if err != nil {
		return "", err
	}
	var emailValue, passwordValue sql.NullString
	if err := db.QueryRowContext(ctx, `select email, password from users where id = 1`).Scan(&emailValue, &passwordValue); err != nil {
		return "", err
	}
	email := emailValue.String
	password := passwordValue.String
	uid := hashid.EncodeUserID(encoder, 1)
	issueDate := time.Now()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
		TokenType: auth.TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uid,
			NotBefore: jwt.NewNumericDate(issueDate),
			ExpiresAt: jwt.NewNumericDate(issueDate.Add(2 * time.Hour)),
		},
		StateHash: authHash(values["siteID"], email, password),
	}).SignedString([]byte(values["secret_key"]))
	if err != nil {
		return "", err
	}
	return token, nil
}

func authHash(siteID, email, password string) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s", email, password, siteID)))
	return sum[:]
}

func buildZip(files map[string]string) ([]byte, error) {
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	for name, content := range files {
		writer, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func minioObjectExists(objectPath string) bool {
	objectPath = strings.TrimLeft(path.Clean("/"+objectPath), "/")
	if objectPath == "" || objectPath == "." {
		return false
	}
	target := path.Join(minioBucketDir, objectPath, "xl.meta")
	cmd := exec.Command("docker", "exec", minioContainer, "test", "-f", target)
	return cmd.Run() == nil
}

func sidecarManifestPath(sidecar *sidecarResponse) string {
	if sidecar == nil || len(sidecar.Objects) == 0 {
		return ""
	}
	needle := fmt.Sprintf("/%d/%d/", sidecar.FileID, sidecar.EntityID)
	for _, item := range sidecar.Objects {
		if item.Path == "" {
			continue
		}
		if idx := strings.Index(item.Path, needle); idx >= 0 {
			return path.Join(item.Path[:idx+len(needle)-1], "manifest.json")
		}
	}
	dir := path.Dir(sidecar.Objects[0].Path)
	for _, item := range sidecar.Objects {
		if item.ID == "content.txt" || item.Kind == "text" {
			dir = path.Dir(item.Path)
			break
		}
	}
	return path.Join(dir, "manifest.json")
}

func findObject(items []sidecarObject, pred func(sidecarObject) bool) (sidecarObject, bool) {
	for _, item := range items {
		if pred(item) {
			return item, true
		}
	}
	return sidecarObject{}, false
}

func parentURI(uri string) string {
	idx := strings.LastIndex(uri, "/")
	if idx <= len("cloudreve://") {
		return uri
	}
	return uri[:idx]
}

func fileName(uri string) string {
	idx := strings.LastIndex(uri, "/")
	if idx < 0 {
		return uri
	}
	return uri[idx+1:]
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func str(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func strSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func anySlice(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	return items
}

func attachmentsContain(value any, key, needle string) bool {
	for _, item := range anySlice(value) {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.Contains(str(m[key]), needle) {
			return true
		}
	}
	return false
}

func attachmentsSourceUsesSidecar(value any) bool {
	for _, item := range anySlice(value) {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.Contains(str(m["source"]), "/attachment-text/") {
			return true
		}
	}
	return false
}

func sidecarMime(name string) string {
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}

var _ = sidecarMime
