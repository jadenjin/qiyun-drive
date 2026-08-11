package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pan/backend/internal/config"
	"pan/backend/internal/storage"
)

type Server struct {
	db    *pgxpool.Pool
	store *storage.Store
	cfg   config.Config
	limit *attemptLimiter
}

type attemptWindow struct {
	count int
	reset time.Time
}

type attemptLimiter struct {
	mu      sync.Mutex
	windows map[string]attemptWindow
}

type actor struct {
	UserID      uuid.UUID
	HouseholdID uuid.UUID
	Username    string
	DisplayName string
	Role        string
}

type contextKey string

const actorKey contextKey = "actor"

func New(db *pgxpool.Pool, store *storage.Store, cfg config.Config) http.Handler {
	s := &Server{db: db, store: store, cfg: cfg, limit: &attemptLimiter{windows: make(map[string]attemptWindow)}}
	r := chi.NewRouter()
	r.Use(s.recoverer, s.requestLog, s.cors)
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	r.Get("/health/ready", s.ready)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/bootstrap", s.bootstrapStatus)
		r.Post("/bootstrap", s.bootstrap)
		r.With(s.limitSensitive).Post("/auth/login", s.login)
		r.Post("/invitations/accept", s.acceptInvitation)
		r.With(s.limitSensitive).Post("/password-resets/complete", s.completePasswordReset)
		r.With(s.limitSensitive).Post("/public/shares/{token}/unlock", s.unlockShare)
		r.Get("/public/shares/{token}", s.publicShare)
		r.Get("/public/shares/{token}/archive", s.publicShareArchive)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/me", s.me)
			r.Patch("/me", s.updateMe)
			r.Post("/me/password", s.changePassword)
			r.Post("/auth/logout", s.logout)
			r.Get("/spaces", s.listSpaces)
			r.Get("/nodes", s.listNodes)
			r.Get("/folders/tree", s.listFolderTree)
			r.Post("/folders", s.createFolder)
			r.Patch("/nodes/{id}", s.renameNode)
			r.Post("/nodes/{id}/move", s.moveNode)
			r.Delete("/nodes/{id}", s.trashNode)
			r.Get("/trash", s.listTrash)
			r.Post("/trash/{id}/restore", s.restoreNode)
			r.Delete("/trash/{id}", s.purgeNode)
			r.Post("/upload-batches", s.createUploadBatch)
			r.Post("/uploads", s.createUpload)
			r.Post("/uploads/{id}/parts", s.presignParts)
			r.Post("/uploads/{id}/complete", s.completeUpload)
			r.Delete("/uploads/{id}", s.abortUpload)
			r.Get("/uploads", s.listUploads)
			r.Get("/nodes/{id}/download", s.downloadFile)
			r.Get("/nodes/{id}/archive", s.downloadArchive)
			r.Get("/photos", s.listPhotos)
			r.Patch("/photos/{id}", s.updatePhoto)
			r.Get("/albums", s.listAlbums)
			r.Post("/albums", s.createAlbum)
			r.Patch("/albums/{id}", s.updateAlbum)
			r.Delete("/albums/{id}", s.deleteAlbum)
			r.Post("/albums/{id}/items", s.addAlbumItems)
			r.Delete("/albums/{id}/items/{nodeID}", s.removeAlbumItem)
			r.Get("/albums/{id}/items", s.listAlbumItems)
			r.Get("/nodes/{id}/permissions", s.getNodePermissions)
			r.Put("/nodes/{id}/permissions", s.setNodePermissions)
			r.Get("/albums/{id}/permissions", s.getAlbumPermissions)
			r.Put("/albums/{id}/permissions", s.setAlbumPermissions)
			r.Post("/shares", s.createShare)
			r.Get("/shares", s.listShares)
			r.Delete("/shares/{id}", s.revokeShare)
			r.Get("/members", s.listMembers)
			r.Post("/invitations", s.createInvitation)
			r.Post("/members/{id}/password-reset", s.createPasswordReset)
			r.Patch("/members/{id}", s.updateMemberRole)
			r.Patch("/spaces/{id}/quota", s.updateQuota)
			r.Get("/admin/audit", s.listAudit)
		})
	})
	return r
}

func (s *Server) limitSensitive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
		if client == "" {
			client, _, _ = net.SplitHostPort(r.RemoteAddr)
		}
		key := client + ":" + r.URL.Path
		now := time.Now()
		s.limit.mu.Lock()
		window := s.limit.windows[key]
		if window.reset.Before(now) {
			window = attemptWindow{reset: now.Add(5 * time.Minute)}
		}
		window.count++
		s.limit.windows[key] = window
		s.limit.mu.Unlock()
		if window.count > 10 {
			w.Header().Set("Retry-After", "300")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请稍后再试")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "数据库尚未就绪")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimRight(r.Header.Get("Origin"), "/")
		if origin != "" && origin == s.cfg.AllowedOrigin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,PUT,DELETE,OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && origin != "" && origin != s.cfg.AllowedOrigin {
			writeError(w, http.StatusForbidden, "origin_forbidden", "请求来源不受信任")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				slog.Error("panic", "error", value)
				writeError(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("pan_session")
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		var a actor
		err = s.db.QueryRow(r.Context(), `
			SELECT u.id, hm.household_id, u.username, u.display_name, hm.role
			FROM sessions s
			JOIN users u ON u.id=s.user_id AND NOT u.disabled
			JOIN household_members hm ON hm.user_id=u.id
			WHERE s.token_hash=$1 AND s.expires_at>now()`, tokenHash(cookie.Value)).Scan(
			&a.UserID, &a.HouseholdID, &a.Username, &a.DisplayName, &a.Role)
		if err != nil {
			clearCookie(w, s.cfg.CookieSecure)
			writeError(w, http.StatusUnauthorized, "unauthorized", "登录已失效")
			return
		}
		_, _ = s.db.Exec(r.Context(), `UPDATE sessions SET last_seen_at=now() WHERE token_hash=$1 AND last_seen_at<now()-interval '10 minutes'`, tokenHash(cookie.Value))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey, a)))
	})
}

func actorFrom(r *http.Request) actor {
	return r.Context().Value(actorKey).(actor)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		slog.Warn("invalid request json", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusBadRequest, "invalid_json", "请求内容格式不正确")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func tokenHash(token string) string {
	return authTokenHash(token)
}

func clearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: "pan_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return uuid.Nil, false
	}
	return id, true
}

func dbNotFound(w http.ResponseWriter, err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return true
	}
	return false
}

func internalError(w http.ResponseWriter, err error) {
	slog.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "操作失败，请稍后重试")
}

func (s *Server) audit(ctx context.Context, a actor, action, resourceType string, resourceID *uuid.UUID, metadata any) {
	payload, _ := json.Marshal(metadata)
	_, _ = s.db.Exec(ctx, `INSERT INTO audit_events(id,household_id,actor_user_id,action,resource_type,resource_id,metadata) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		uuid.New(), a.HouseholdID, a.UserID, action, resourceType, resourceID, payload)
}

func normalizeUsername(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func validatePassword(value string) error {
	if len([]rune(value)) < 10 {
		return fmt.Errorf("密码至少需要 10 个字符")
	}
	return nil
}
