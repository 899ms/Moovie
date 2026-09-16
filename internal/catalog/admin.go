package catalog

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"
	"time"

	"github.com/TwoThreeWang/Moovie/new/internal/platform/database"
	"github.com/TwoThreeWang/Moovie/new/internal/workqueue"
)

// AdminMedia 是后台列表和详情共用的轻量媒体状态，不读取 768 维向量本身。
type AdminMedia struct {
	ID                 int
	DoubanID           string
	Title              string
	Year               string
	MediaType          string
	Summary            string
	MetadataStatus     string
	CompletenessScore  int
	HasSemanticContent bool
	HasEmbedding       bool
	SemanticHash       string
	LastMetadataSyncAt *time.Time
	NextRefreshAt      *time.Time
	UpdatedAt          time.Time
	ResourceCount      int
	ExternalIDs        []AdminExternalID
	Sources            []AdminMediaSource
	Jobs               []AdminMediaJob
}

type AdminExternalID struct{ Provider, ExternalType, ExternalID string }

type AdminMediaSource struct {
	Provider       string
	FetchedAt      time.Time
	UnchangedCount int
	ErrorMessage   string
}

type AdminMediaJob struct {
	ID                        int
	TaskType, Reason, Status  string
	AttemptCount, MaxAttempts int
	ErrorMessage              string
	UpdatedAt                 time.Time
}

const adminMediaColumns = `m.id, m.douban_id, m.title, m.year, m.media_type,
m.summary, m.metadata_status, m.completeness_score,
m.embedding_content <> '', m.embedding IS NOT NULL, m.semantic_hash,
m.last_metadata_sync_at, m.next_refresh_at, m.updated_at,
(SELECT COUNT(*) FROM resource_media_links link WHERE link.media_id = m.id)`

// SearchAdminMedia 按豆瓣 ID 或标题查询媒体。
func (store *PostgresStore) SearchAdminMedia(ctx context.Context, keyword string, limit int) ([]AdminMedia, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := store.database.Query(ctx, `SELECT `+adminMediaColumns+` FROM media m
WHERE m.douban_id = $1 OR m.title ILIKE $2 OR m.original_title ILIKE $2
ORDER BY CASE WHEN m.douban_id = $1 THEN 0 WHEN LOWER(m.title) = LOWER($1) THEN 1 ELSE 2 END,
         m.updated_at DESC, m.id DESC
LIMIT $3`, keyword, "%"+keyword+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("search admin media: %w", err)
	}
	defer rows.Close()
	items := make([]AdminMedia, 0, limit)
	for rows.Next() {
		item, err := scanAdminMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("scan admin media: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// FindAdminMedia 返回一条媒体的诊断信息。
func (store *PostgresStore) FindAdminMedia(ctx context.Context, mediaID int) (*AdminMedia, error) {
	rows, err := store.database.Query(ctx, `SELECT `+adminMediaColumns+` FROM media m WHERE m.id = $1`, mediaID)
	if err != nil {
		return nil, fmt.Errorf("find admin media: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	item, err := scanAdminMedia(rows)
	if err != nil {
		return nil, fmt.Errorf("scan admin media: %w", err)
	}
	rows.Close()
	if err := store.loadAdminMediaDetails(ctx, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func scanAdminMedia(row interface{ Scan(...any) error }) (AdminMedia, error) {
	var item AdminMedia
	err := row.Scan(&item.ID, &item.DoubanID, &item.Title, &item.Year, &item.MediaType,
		&item.Summary, &item.MetadataStatus, &item.CompletenessScore,
		&item.HasSemanticContent, &item.HasEmbedding, &item.SemanticHash,
		&item.LastMetadataSyncAt, &item.NextRefreshAt, &item.UpdatedAt, &item.ResourceCount)
	return item, err
}

func (store *PostgresStore) loadAdminMediaDetails(ctx context.Context, item *AdminMedia) error {
	externalRows, err := store.database.Query(ctx, `SELECT provider, external_type, external_id
FROM media_external_ids WHERE media_id = $1 ORDER BY provider, external_type`, item.ID)
	if err != nil {
		return fmt.Errorf("list media external IDs: %w", err)
	}
	for externalRows.Next() {
		var external AdminExternalID
		if err := externalRows.Scan(&external.Provider, &external.ExternalType, &external.ExternalID); err != nil {
			externalRows.Close()
			return fmt.Errorf("scan media external ID: %w", err)
		}
		item.ExternalIDs = append(item.ExternalIDs, external)
	}
	externalErr := externalRows.Err()
	externalRows.Close()
	if externalErr != nil {
		return externalErr
	}

	sourceRows, err := store.database.Query(ctx, `SELECT provider, fetched_at, unchanged_count, error_message
FROM media_source_snapshots WHERE media_id = $1 ORDER BY fetched_at DESC`, item.ID)
	if err != nil {
		return fmt.Errorf("list media sources: %w", err)
	}
	for sourceRows.Next() {
		var source AdminMediaSource
		if err := sourceRows.Scan(&source.Provider, &source.FetchedAt,
			&source.UnchangedCount, &source.ErrorMessage); err != nil {
			sourceRows.Close()
			return fmt.Errorf("scan media source: %w", err)
		}
		item.Sources = append(item.Sources, source)
	}
	sourceErr := sourceRows.Err()
	sourceRows.Close()
	if sourceErr != nil {
		return sourceErr
	}

	jobRows, err := store.database.Query(ctx, `SELECT id, task_type, reason, status, attempt_count, max_attempts,
error_message, updated_at FROM worker_jobs WHERE subject_key = $1
AND task_type IN ('douban_metadata','tmdb','semantic_content','embedding') ORDER BY id DESC LIMIT 12`, item.DoubanID)
	if err != nil {
		return fmt.Errorf("list media jobs: %w", err)
	}
	defer jobRows.Close()
	for jobRows.Next() {
		var job AdminMediaJob
		if err := jobRows.Scan(&job.ID, &job.TaskType, &job.Reason, &job.Status,
			&job.AttemptCount, &job.MaxAttempts, &job.ErrorMessage, &job.UpdatedAt); err != nil {
			return fmt.Errorf("scan media job: %w", err)
		}
		item.Jobs = append(item.Jobs, job)
	}
	return jobRows.Err()
}

// RegenerateSemanticContent 让旧向量继续服务，直到新语义文本保存时再原子清空。
func (store *PostgresStore) RegenerateSemanticContent(ctx context.Context, mediaID, requestedBy int) (int, error) {
	var jobID int
	err := database.InTransaction(ctx, store.database, func(db database.Executor) error {
		var doubanID, semanticHash string
		if err := db.QueryRow(ctx, `UPDATE media SET embedding_content = ''
WHERE id = $1 AND douban_id <> '' AND metadata_status <> 'partial' AND completeness_score >= 70
RETURNING douban_id, semantic_hash`, mediaID).Scan(&doubanID, &semanticHash); err != nil {
			return fmt.Errorf("media is not ready for semantic regeneration: %w", err)
		}
		var err error
		jobID, err = workqueue.NewPostgresStore(db).Enqueue(ctx, workqueue.Spec{
			TaskType: RefreshProviderSemantic, SubjectKey: doubanID,
			Payload: map[string]string{"douban_id": doubanID, "semantic_hash": semanticHash},
			Reason:  "manual", RequestedBy: requestedBy,
		})
		return err
	})
	return jobID, err
}

// RegenerateEmbedding 清空当前向量并用已经保存的语义文本重新生成。
func (store *PostgresStore) RegenerateEmbedding(ctx context.Context, mediaID, requestedBy int) (int, error) {
	var jobID int
	err := database.InTransaction(ctx, store.database, func(db database.Executor) error {
		var doubanID, content string
		if err := db.QueryRow(ctx, `UPDATE media SET embedding = NULL
WHERE id = $1 AND douban_id <> '' AND embedding_content <> ''
RETURNING douban_id, embedding_content`, mediaID).Scan(&doubanID, &content); err != nil {
			return fmt.Errorf("media has no semantic content for embedding regeneration: %w", err)
		}
		contentHash := fmt.Sprintf("%x", md5.Sum([]byte(content)))
		var err error
		jobID, err = workqueue.NewPostgresStore(db).Enqueue(ctx, workqueue.Spec{
			TaskType: RefreshProviderEmbedding, SubjectKey: doubanID,
			Payload: map[string]string{"douban_id": doubanID, "content_hash": contentHash},
			Reason:  "manual", RequestedBy: requestedBy,
		})
		return err
	})
	return jobID, err
}
