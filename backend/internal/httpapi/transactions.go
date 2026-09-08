package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type databaseHandle interface {
	permissionQuerier
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Begin(context.Context) (pgx.Tx, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type requestTransaction struct{ pgx.Tx }

func (t requestTransaction) BeginTx(ctx context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return t.Tx.Begin(ctx)
}

type deferredResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *deferredResponse) Header() http.Header { return w.header }
func (w *deferredResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *deferredResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

// All permission reads, state transitions and audit events of a mutation use
// one serializable transaction. Nested handler transactions become savepoints.
// Success and cookies are withheld until the outer commit has succeeded.
func (s *Server) write(handler func(*Server, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tx, err := s.db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			internalError(w, err)
			return
		}
		committed := false
		rollbackActions := []func(context.Context){}
		defer func() {
			_ = tx.Rollback(context.WithoutCancel(r.Context()))
			if !committed {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
				defer cancel()
				for _, action := range rollbackActions {
					action(cleanup)
				}
			}
		}()
		requestServer := *s
		requestServer.db = requestTransaction{tx}
		requestServer.onRollback = &rollbackActions
		var mutationError error
		requestServer.mutationError = &mutationError
		if existing, ok := r.Context().Value(actorKey).(actor); ok {
			cookie, cookieErr := r.Cookie("pan_session")
			var valid bool
			if cookieErr != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "登录已失效")
				return
			}
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>now())`, tokenHash(cookie.Value), existing.UserID).Scan(&valid); err != nil {
				internalError(w, err)
				return
			}
			if !valid {
				writeError(w, http.StatusUnauthorized, "unauthorized", "登录已失效")
				return
			}
			current, err := requestServer.actorForUser(r.Context(), existing.UserID)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "登录已失效")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), actorKey, current))
		}
		buffer := &deferredResponse{header: make(http.Header)}
		handler(&requestServer, buffer, r)
		if mutationError != nil {
			if isConcurrentChange(mutationError) {
				writeError(w, http.StatusConflict, "concurrent_change", "内容正在变化，请刷新后重试")
			} else {
				internalError(w, mutationError)
			}
			return
		}
		if buffer.status == 0 {
			buffer.status = http.StatusOK
		}
		if buffer.status >= 200 && buffer.status < 400 {
			if err := tx.Commit(r.Context()); err != nil {
				if isConcurrentChange(err) {
					writeError(w, http.StatusConflict, "concurrent_change", "内容正在变化，请刷新后重试")
				} else {
					internalError(w, err)
				}
				return
			}
			committed = true
		}
		for key, values := range buffer.header {
			w.Header()[key] = values
		}
		w.WriteHeader(buffer.status)
		_, _ = w.Write(buffer.body.Bytes())
	}
}
