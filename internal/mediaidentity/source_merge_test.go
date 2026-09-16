package mediaidentity

import (
	"strings"
	"testing"
	"time"

	"github.com/TwoThreeWang/Moovie/new/internal/platform/database/testdb"
)

func TestSemanticChangeClearsTextButKeepsOldVectorAndQueuesGatewayWork(t *testing.T) {
	pool := testdb.Pool(t)
	store := NewPostgresStore(pool)
	media := Media{
		DoubanID: "1292052", MediaType: "movie", Title: "肖申克的救赎", Year: "1994",
		Poster: "poster", Summary: "旧简介", Genres: "剧情", Countries: "美国",
		Directors: `[{"name":"弗兰克"}]`, Actors: `[{"name":"蒂姆"}]`, Duration: "142分钟",
	}
	merged, err := store.MergeSource(t.Context(), "douban", media, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	vector := "[" + strings.Repeat("0,", 767) + "0]"
	if _, err := pool.Exec(t.Context(), `UPDATE media SET embedding_content='old semantic text', embedding=$2::vector WHERE id=$1`, merged.ID, vector); err != nil {
		t.Fatal(err)
	}
	media.Summary = "新简介"
	if _, err := store.MergeSource(t.Context(), "douban", media, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	var content string
	var hasVector bool
	if err := pool.QueryRow(t.Context(), `SELECT embedding_content, embedding IS NOT NULL FROM media WHERE id=$1`, merged.ID).Scan(&content, &hasVector); err != nil {
		t.Fatal(err)
	}
	if content != "" || !hasVector {
		t.Fatalf("semantic invalidation = content:%q vector:%t", content, hasVector)
	}
	var jobs int
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM worker_jobs WHERE task_type='semantic_content' AND subject_key='1292052' AND status IN ('pending','running')`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("semantic jobs = %d, want 1", jobs)
	}
}

func TestSourceFieldsKeepProviderPriorityBoundaries(t *testing.T) {
	douban := sourceFields("douban", Media{MediaType: "season", Title: "中文名", OriginalTitle: "旧原名", Backdrops: "ignored", RatingDouban: 9.2})
	priority := make(map[string]int, len(douban))
	for _, field := range douban {
		priority[field.column] = field.priority
	}
	if priority["title"] != 100 || priority["summary"] != 100 || priority["rating_douban"] != 100 {
		t.Fatalf("Douban priorities = %#v", priority)
	}
	if priority["media_type"] != 100 {
		t.Fatalf("Douban media type priority = %d", priority["media_type"])
	}
	if priority["backdrops"] != 0 || priority["rating_tmdb"] != 0 {
		t.Fatalf("Douban claimed TMDB-only fields: %#v", priority)
	}

	tmdb := sourceFields("tmdb", Media{OriginalTitle: "Original", Duration: "120分钟", RatingTMDB: 8.4})
	for _, field := range tmdb {
		if field.column == "original_title" && field.priority != 100 {
			t.Fatalf("TMDB original title priority = %d", field.priority)
		}
		if field.column == "duration" && field.priority != 100 {
			t.Fatalf("TMDB duration priority = %d", field.priority)
		}
	}
	manual := sourceFields("manual", Media{Poster: "pinned"})
	for _, field := range manual {
		if field.column == "poster" && field.priority != 1000 {
			t.Fatalf("manual poster priority = %d", field.priority)
		}
	}
}

func TestNormalizeMediaTypeUsesCanonicalIdentityNamespaces(t *testing.T) {
	tests := map[string]string{
		"movie": "movie", "film": "movie", "": "",
		"tv": "tv", "series": "tv", "season": "tv", "show": "show",
		"animation": "cartoon", "cartoon": "cartoon",
	}
	for input, expected := range tests {
		if got := normalizeMediaType(input); got != expected {
			t.Fatalf("normalizeMediaType(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestMetadataRefreshStateUsesStableHashCompletenessAndCappedBackoff(t *testing.T) {
	state := mergedMetadataState{MediaType: "movie", Title: "标题", Year: "2026", Poster: "poster", Summary: "简介", Genres: "剧情", Countries: "中国", Directors: "导演", Actors: "演员", Duration: "120分钟", ExternalIDs: 2, MediaUnits: 1}
	if first, second := stableJSONHash(state), stableJSONHash(state); first == "" || first != second {
		t.Fatalf("unstable content hash: %q/%q", first, second)
	}
	if score := metadataCompleteness(state); score != 100 {
		t.Fatalf("completeness = %d", score)
	}
	if delay := metadataRefreshDelay(0, 100); delay != 24*time.Hour {
		t.Fatalf("initial delay = %s", delay)
	}
	if delay := metadataRefreshDelay(20, 100); delay != 90*24*time.Hour {
		t.Fatalf("capped delay = %s", delay)
	}
	// 资料不全时前几次加急到一天，但内容连续没变化后必须退回正常退避，
	// 否则永远补不齐的条目会每天重新入队。
	if delay := metadataRefreshDelay(1, 60); delay != 24*time.Hour {
		t.Fatalf("partial delay = %s", delay)
	}
	if delay := metadataRefreshDelay(20, 60); delay != 90*24*time.Hour {
		t.Fatalf("settled partial delay = %s", delay)
	}
}

func TestSourceFieldsIgnoreBlankValuesAtMergeBoundary(t *testing.T) {
	fields := sourceFields("tmdb", Media{Title: "", Poster: "", RatingTMDB: 0})
	eligible := 0
	for _, field := range fields {
		if field.text != "" && field.priority > 0 {
			eligible++
		}
	}
	if eligible != 0 {
		t.Fatalf("blank source unexpectedly produced %d mergeable fields", eligible)
	}
}
