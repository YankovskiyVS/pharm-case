package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"
	"time"
)

type pageResult struct {
	Items       []json.RawMessage `json:"items"`
	Total       int64             `json:"total"`
	NextCursor  *string           `json:"next_cursor"`
	UnreadCount *int64            `json:"unread_count,omitempty"`
}
type pageCursor struct {
	Time time.Time `json:"t"`
	ID   string    `json:"i"`
}

func pagination(r *http.Request) (int, *pageCursor, error) {
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return 0, nil, errors.New("limit must be 1..100")
		}
		limit = n
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(data) > 256 {
			return 0, nil, errors.New("invalid cursor")
		}
		var cursor pageCursor
		if json.Unmarshal(data, &cursor) != nil || !uuid(cursor.ID) || cursor.Time.IsZero() {
			return 0, nil, errors.New("invalid cursor")
		}
		return limit, &cursor, nil
	}
	return limit, nil, nil
}
func encodeCursor(t time.Time, id string) string {
	b, err := json.Marshal(pageCursor{Time: t, ID: id})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// pageQuery executes a bounded stable cursor page and total in one repeatable-read snapshot.
// base is a trusted static query exposing id(uuid) and created_at(timestamptz).
func (s *Server) pageQuery(ctx context.Context, base string, args []any, limit int, cursor *pageCursor) (pageResult, error) {
	result := pageResult{Items: []json.RawMessage{}}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer rollback(tx, ctx)
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM ("+base+") page_total", args...).Scan(&result.Total); err != nil {
		return result, err
	}
	var at *time.Time
	id := "00000000-0000-0000-0000-000000000000"
	if cursor != nil {
		at = &cursor.Time
		id = cursor.ID
	}
	n := len(args) + 1
	query := fmt.Sprintf(`SELECT to_jsonb(x),x.created_at,x.id::text FROM (%s) x WHERE ($%d::timestamptz IS NULL OR (x.created_at,x.id)<($%d::timestamptz,$%d::uuid)) ORDER BY x.created_at DESC,x.id DESC LIMIT $%d`, base, n, n, n+1, n+2)
	values := append(append([]any{}, args...), at, id, limit+1)
	rows, err := tx.Query(ctx, query, values...)
	if err != nil {
		return result, err
	}
	times := []time.Time{}
	ids := []string{}
	for rows.Next() {
		var raw []byte
		var t time.Time
		var id string
		if err = rows.Scan(&raw, &t, &id); err != nil {
			break
		}
		result.Items = append(result.Items, json.RawMessage(raw))
		times = append(times, t)
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
		next := encodeCursor(times[limit-1], ids[limit-1])
		result.NextCursor = &next
	}
	return result, tx.Commit(ctx)
}
func (s *Server) listPage(w http.ResponseWriter, r *http.Request, base string, args ...any) {
	limit, cursor, err := pagination(r)
	if err != nil {
		writeError(w, 400, "Проверьте размер страницы и курсор")
		return
	}
	result, err := s.pageQuery(r.Context(), base, args, limit, cursor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}

func readSnapshotOptions() pgx.TxOptions {
	return pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
}
