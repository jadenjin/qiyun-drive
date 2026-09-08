package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"pan/backend/internal/config"
	"pan/backend/internal/storage"
)

type Server struct {
	db            databaseHandle
	eventDB       databaseHandle
	store         *storage.Store
	cfg           config.Config
	limit         *attemptLimiter
	passwordSlots chan struct{}
	archiveSlots  chan struct{}
	onRollback    *[]func(context.Context)
	mutationError *error
}

type attemptWindow struct {
	failures int
	reset    time.Time
}

type attemptLimiter struct {
	mu          sync.Mutex
	windows     map[string]attemptWindow
	window      time.Duration
	maxFailures int
	maxEntries  int
	lastCleanup time.Time
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
	s := &Server{db: db, eventDB: db, store: store, cfg: cfg, limit: newAttemptLimiter(), passwordSlots: make(chan struct{}, 2), archiveSlots: make(chan struct{}, 2)}
	r := chi.NewRouter()
	r.Use(s.recoverer, s.securityHeaders, s.requestLog, s.cors)
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	r.Get("/health/ready", s.ready)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/bootstrap", s.bootstrapStatus)
		r.With(s.limitSensitive, s.limitPasswordWork).Post("/bootstrap", s.write((*Server).bootstrap))
		r.With(s.limitSensitive, s.limitPasswordWork).Post("/auth/login", s.write((*Server).login))
		r.With(s.limitSensitive, s.limitPasswordWork).Post("/invitations/accept", s.write((*Server).acceptInvitation))
		r.With(s.limitSensitive, s.limitPasswordWork).Post("/password-resets/complete", s.write((*Server).completePasswordReset))
		r.With(s.limitSensitive, s.limitPasswordWork).Post("/public/shares/{token}/unlock", s.write((*Server).unlockShare))
		r.Get("/public/shares/{token}", s.publicShare)
		r.With(s.limitArchive).Post("/public/shares/{token}/archive", s.publicShareArchive)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/me", s.me)
			r.Patch("/me", s.write((*Server).updateMe))
			r.With(s.limitSensitive, s.limitPasswordWork).Post("/me/password", s.write((*Server).changePassword))
			r.Get("/me/sessions", s.listSessions)
			r.Get("/me/security", s.accountSecurity)
			r.With(s.limitSensitive, s.limitPasswordWork).Post("/me/totp/setup", s.write((*Server).beginTOTP))
			r.With(s.limitSensitive).Post("/me/totp/confirm", s.write((*Server).confirmTOTP))
			r.With(s.limitSensitive, s.limitPasswordWork).Post("/me/totp/disable", s.write((*Server).disableTOTP))
			r.Delete("/me/sessions/{id}", s.write((*Server).revokeSession))
			r.Post("/auth/logout", s.write((*Server).logout))
			r.Get("/spaces", s.listSpaces)
			r.Get("/nodes", s.listNodes)
			r.Get("/folders/tree", s.listFolderTree)
			r.Post("/folders", s.write((*Server).createFolder))
			r.Patch("/nodes/{id}", s.write((*Server).renameNode))
			r.Post("/nodes/{id}/move", s.write((*Server).moveNode))
			r.Delete("/nodes/{id}", s.write((*Server).trashNode))
			r.Get("/trash", s.listTrash)
			r.Post("/trash/{id}/restore", s.write((*Server).restoreNode))
			r.Delete("/trash/{id}", s.write((*Server).purgeNode))
			r.Post("/upload-batches", s.write((*Server).createUploadBatch))
			r.Post("/uploads", s.write((*Server).createUpload))
			r.Post("/uploads/{id}/parts", s.write((*Server).presignParts))
			r.Post("/uploads/{id}/resume", s.write((*Server).resumeUpload))
			r.Post("/uploads/{id}/complete", s.write((*Server).completeUpload))
			r.Delete("/uploads/{id}", s.write((*Server).abortUpload))
			r.Get("/uploads", s.listUploads)
			r.Get("/nodes/{id}/download", s.downloadFile)
			r.Get("/nodes/{id}/preview", s.previewFile)
			r.With(s.limitArchive).Get("/nodes/{id}/archive", s.downloadArchive)
			r.Get("/photos", s.listPhotos)
			r.Patch("/photos/{id}", s.write((*Server).updatePhoto))
			r.Get("/albums", s.listAlbums)
			r.Post("/albums", s.write((*Server).createAlbum))
			r.Patch("/albums/{id}", s.write((*Server).updateAlbum))
			r.Delete("/albums/{id}", s.write((*Server).deleteAlbum))
			r.Post("/albums/{id}/items", s.write((*Server).addAlbumItems))
			r.Delete("/albums/{id}/items/{nodeID}", s.write((*Server).removeAlbumItem))
			r.Get("/albums/{id}/items", s.listAlbumItems)
			r.Get("/nodes/{id}/permissions", s.getNodePermissions)
			r.Put("/nodes/{id}/permissions", s.write((*Server).setNodePermissions))
			r.Get("/albums/{id}/permissions", s.getAlbumPermissions)
			r.Put("/albums/{id}/permissions", s.write((*Server).setAlbumPermissions))
			r.With(s.limitPasswordWork).Post("/shares", s.write((*Server).createShare))
			r.Get("/shares", s.listShares)
			r.Delete("/shares/{id}", s.write((*Server).revokeShare))
			r.Get("/members", s.listMembers)
			r.Post("/invitations", s.write((*Server).createInvitation))
			r.Post("/members/{id}/password-reset", s.write((*Server).createPasswordReset))
			r.Patch("/members/{id}", s.write((*Server).updateMemberRole))
			r.Patch("/spaces/{id}/quota", s.write((*Server).updateQuota))
			r.Get("/admin/audit", s.listAudit)
			r.Get("/admin/operations", s.operations)
		})
	})
	return r
}

// Bound aggregate Argon2 memory even when requests arrive concurrently or
// through many addresses. Reject excess work rather than building a queue.
func (s *Server) limitPasswordWork(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.passwordSlots <- struct{}{}:
			defer func() { <-s.passwordSlots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusTooManyRequests, "busy", "服务繁忙，请稍后重试")
		}
	})
}

func (s *Server) limitSensitive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// All share tokens share one budget; rotating token paths must not
		// create an unlimited number of independent authentication budgets.
		key := s.clientIP(r) + ":" + safeLogPath(r.URL.Path)
		now := time.Now()
		allowed, retryAfter := s.limit.allowed(key, now)
		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(math.Ceil(retryAfter.Seconds()))))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请稍后再试")
			return
		}
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		if recorder.status >= 200 && recorder.status < 400 {
			s.limit.clear(key)
		} else if recorder.status == http.StatusUnauthorized || recorder.status == http.StatusForbidden || recorder.status == http.StatusNotFound || recorder.status == http.StatusGone {
			s.limit.recordFailure(key, time.Now())
		}
	})
}

func newAttemptLimiter() *attemptLimiter {
	return &attemptLimiter{windows: make(map[string]attemptWindow), window: 5 * time.Minute, maxFailures: 10, maxEntries: 10000}
}

func (l *attemptLimiter) allowed(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)
	window, ok := l.windows[key]
	if !ok || !window.reset.After(now) {
		return true, 0
	}
	if window.failures >= l.maxFailures {
		return false, window.reset.Sub(now)
	}
	return true, 0
}

func (l *attemptLimiter) recordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(now)
	window, ok := l.windows[key]
	if !ok || !window.reset.After(now) {
		if len(l.windows) >= l.maxEntries {
			for candidate := range l.windows {
				delete(l.windows, candidate)
				break
			}
		}
		window = attemptWindow{reset: now.Add(l.window)}
	}
	window.failures++
	l.windows[key] = window
}

func (l *attemptLimiter) clear(key string) {
	l.mu.Lock()
	delete(l.windows, key)
	l.mu.Unlock()
}

func (l *attemptLimiter) cleanupLocked(now time.Time) {
	if len(l.windows) < l.maxEntries && l.lastCleanup.Add(time.Minute).After(now) {
		return
	}
	for key, window := range l.windows {
		if !window.reset.After(now) {
			delete(l.windows, key)
		}
	}
	l.lastCleanup = now
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		// This deployment trusts exactly one in-network reverse proxy. Read the
		// rightmost valid hop so a client-supplied prefix cannot bypass rate
		// limits if a proxy appends instead of replacing X-Forwarded-For.
		forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for index := len(forwarded) - 1; index >= 0; index-- {
			if candidate := strings.TrimSpace(forwarded[index]); net.ParseIP(candidate) != nil {
				return candidate
			}
		}
	}
	client, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && client != "" {
		return client
	}
	return r.RemoteAddr
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	written, err := w.ResponseWriter.Write(body)
	w.bytes += written
	return written, err
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "base-uri 'none'; frame-ancestors 'none'; object-src 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var alive int
	if err := s.db.QueryRow(ctx, "SELECT 1").Scan(&alive); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "数据库尚未就绪")
		return
	}
	if err := s.store.Ready(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "对象存储尚未就绪")
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
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		slog.Info("request", "request_id", requestID, "method", r.Method, "path", safeLogPath(r.URL.Path), "status", status, "bytes", recorder.bytes, "duration_ms", time.Since(started).Milliseconds())
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

func (s *Server) actorForUser(ctx context.Context, userID uuid.UUID) (actor, error) {
	var a actor
	err := s.db.QueryRow(ctx, `
		SELECT u.id,hm.household_id,u.username,u.display_name,hm.role
		FROM users u JOIN household_members hm ON hm.user_id=u.id
		WHERE u.id=$1 AND NOT u.disabled`, userID).Scan(&a.UserID, &a.HouseholdID, &a.Username, &a.DisplayName, &a.Role)
	return a, err
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		slog.Warn("invalid request json", "method", r.Method, "path", safeLogPath(r.URL.Path), "error", err)
		writeError(w, http.StatusBadRequest, "invalid_json", "请求内容格式不正确")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		slog.Warn("invalid trailing json", "method", r.Method, "path", safeLogPath(r.URL.Path), "error", err)
		writeError(w, http.StatusBadRequest, "invalid_json", "请求内容只能包含一个 JSON 对象")
		return false
	}
	return true
}

func safeLogPath(raw string) string {
	const sharePrefix = "/api/v1/public/shares/"
	if !strings.HasPrefix(raw, sharePrefix) {
		return raw
	}
	remainder := strings.TrimPrefix(raw, sharePrefix)
	if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
		return sharePrefix + "[redacted]" + remainder[slash:]
	}
	return sharePrefix + "[redacted]"
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
	// #nosec G124 -- Secure mirrors the validated deployment scheme so the
	// deletion cookie exactly matches the cookie being removed.
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

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}

func isConcurrentChange(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

func internalError(w http.ResponseWriter, err error) {
	if isConcurrentChange(err) {
		writeError(w, http.StatusConflict, "concurrent_change", "内容正在变化，请刷新后重试")
		return
	}
	slog.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "操作失败，请稍后重试")
}

func (s *Server) audit(ctx context.Context, a actor, action, resourceType string, resourceID *uuid.UUID, metadata any) {
	s.auditForSpace(ctx, a, action, resourceType, resourceID, nil, metadata)
}

func (s *Server) auditForSpace(ctx context.Context, a actor, action, resourceType string, resourceID, spaceID *uuid.UUID, metadata any) {
	payload, err := json.Marshal(metadata)
	if err == nil {
		_, err = s.db.Exec(ctx, `INSERT INTO audit_events(id,household_id,actor_user_id,action,resource_type,resource_id,metadata,visibility) VALUES($1,$2,$3,$4,$5,$6,$7,audit_visibility($2,$5,$6,$8))`, uuid.New(), a.HouseholdID, a.UserID, action, resourceType, resourceID, payload, spaceID)
	}
	if err != nil {
		slog.Error("audit write failed", "action", action, "error", err)
		if s.mutationError != nil {
			*s.mutationError = err
		}
	}
}

func normalizeUsername(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

func validUsername(value string) bool { return usernamePattern.MatchString(value) }

func validatePassword(value string) error {
	length := len([]rune(value))
	if length < 10 {
		return fmt.Errorf("密码至少需要 10 个字符")
	}
	if length > 128 {
		return fmt.Errorf("密码不能超过 128 个字符")
	}
	return nil
}
