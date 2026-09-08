package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	panAuth "pan/backend/internal/auth"
)

func securityAgent(value string) string {
	value = strings.ToValidUTF8(value, "")
	chars := []rune(value)
	if len(chars) > 512 {
		chars = chars[:512]
	}
	return string(chars)
}

func deviceLabel(agent string) string {
	os := "未知设备"
	switch {
	case strings.Contains(agent, "Android"):
		os = "Android"
	case strings.Contains(agent, "iPhone") || strings.Contains(agent, "iPad"):
		os = "iPhone / iPad"
	case strings.Contains(agent, "Windows"):
		os = "Windows"
	case strings.Contains(agent, "Macintosh"):
		os = "Mac"
	case strings.Contains(agent, "Linux"):
		os = "Linux"
	}
	browser := "浏览器"
	switch {
	case strings.Contains(agent, "Edg/"):
		browser = "Edge"
	case strings.Contains(agent, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(agent, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(agent, "Safari/"):
		browser = "Safari"
	}
	return os + " · " + browser
}

func sourceLabel(value string) string {
	ip := net.ParseIP(value)
	if ip == nil {
		return "来源未知"
	}
	if ip.IsLoopback() {
		return "本机或代理入口"
	}
	if ip.IsPrivate() {
		return "内网来源"
	}
	return "公网来源"
}

func (s *Server) securityEvent(r *http.Request, userID uuid.UUID, kind string) error {
	_, err := s.db.Exec(r.Context(), `INSERT INTO security_events(id,user_id,kind,source_ip,user_agent) VALUES($1,$2,$3,$4,$5)`, uuid.New(), userID, kind, s.clientIP(r), securityAgent(r.UserAgent()))
	return err
}

// Failed authentication rolls back its business transaction. Persist its
// security event only after that rollback, without retaining request secrets.
func (s *Server) failedSecurityEvent(r *http.Request, userID uuid.UUID, kind string) {
	if userID == uuid.Nil || s.onRollback == nil {
		return
	}
	ip, agent := s.clientIP(r), securityAgent(r.UserAgent())
	*s.onRollback = append(*s.onRollback, func(ctx context.Context) {
		_, err := s.eventDB.Exec(ctx, `INSERT INTO security_events(id,user_id,kind,source_ip,user_agent) VALUES($1,$2,$3,$4,$5)`, uuid.New(), userID, kind, ip, agent)
		if err != nil {
			slog.Error("security event write failed", "kind", kind, "error", err)
		}
	})
}

func (s *Server) accountSecurity(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var enabled bool
	var remaining int
	if err := s.db.QueryRow(r.Context(), `SELECT totp_secret IS NOT NULL,(SELECT count(*) FROM recovery_codes WHERE user_id=$1) FROM users WHERE id=$1`, a.UserID).Scan(&enabled, &remaining); err != nil {
		internalError(w, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT kind,source_ip,user_agent,created_at FROM security_events WHERE user_id=$1 ORDER BY created_at DESC LIMIT 50`, a.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var kind, ip, agent string
		var created time.Time
		if err := rows.Scan(&kind, &ip, &agent, &created); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"kind": kind, "sourceIp": ip, "sourceLabel": sourceLabel(ip), "device": deviceLabel(agent), "createdAt": created})
	}
	if err := rows.Err(); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled, "available": s.cfg.MFAEncryptionKey != "", "recoveryCodesRemaining": remaining, "events": items})
}

func (s *Server) requireOwnPassword(r *http.Request, password string) bool {
	if len([]rune(password)) > 128 {
		return false
	}
	var hash string
	err := s.db.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE id=$1 AND NOT disabled`, actorFrom(r).UserID).Scan(&hash)
	return err == nil && panAuth.VerifyPassword(hash, password)
}

func (s *Server) beginTOTP(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !s.requireOwnPassword(r, input.Password) {
		s.failedSecurityEvent(r, a.UserID, "mfa.setup_denied")
		writeError(w, 401, "wrong_password", "当前密码不正确")
		return
	}
	if s.cfg.MFAEncryptionKey == "" {
		writeError(w, 503, "mfa_unavailable", "管理员尚未配置二次验证密钥")
		return
	}
	secret, err := panAuth.NewTOTPSecret()
	if err != nil {
		internalError(w, err)
		return
	}
	encrypted, err := panAuth.EncryptTOTP(s.cfg.MFAEncryptionKey, a.UserID.String(), secret)
	if err != nil {
		internalError(w, err)
		return
	}
	result, err := s.db.Exec(r.Context(), `UPDATE users SET totp_pending_secret=$2,totp_pending_expires=now()+interval '10 minutes' WHERE id=$1 AND totp_secret IS NULL`, a.UserID, encrypted)
	if err != nil {
		internalError(w, err)
		return
	}
	if result.RowsAffected() != 1 {
		writeError(w, 409, "mfa_enabled", "请先关闭已有二次验证")
		return
	}
	uri := "otpauth://totp/" + url.PathEscape("栖云:"+a.Username) + "?" + url.Values{"secret": {secret}, "issuer": {"栖云"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
	writeJSON(w, 200, map[string]any{"secret": secret, "uri": uri, "expiresInSeconds": 600})
}

func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var encrypted []byte
	if err := s.db.QueryRow(r.Context(), `SELECT totp_pending_secret FROM users WHERE id=$1 AND totp_secret IS NULL AND totp_pending_expires>now()`, a.UserID).Scan(&encrypted); err != nil {
		writeError(w, 410, "mfa_setup_expired", "设置已过期，请重新开始")
		return
	}
	secret, err := panAuth.DecryptTOTP(s.cfg.MFAEncryptionKey, a.UserID.String(), encrypted)
	if err != nil {
		internalError(w, err)
		return
	}
	step, ok := panAuth.MatchTOTP(secret, input.Code, time.Now(), -1)
	if !ok {
		s.failedSecurityEvent(r, a.UserID, "mfa.code_denied")
		writeError(w, 401, "invalid_code", "验证码不正确或已过期")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE users SET totp_secret=totp_pending_secret,totp_pending_secret=NULL,totp_pending_expires=NULL,totp_last_step=$2 WHERE id=$1`, a.UserID, step); err != nil {
		internalError(w, err)
		return
	}
	codes := make([]string, 0, 8)
	if _, err := s.db.Exec(r.Context(), `DELETE FROM recovery_codes WHERE user_id=$1`, a.UserID); err != nil {
		internalError(w, err)
		return
	}
	for i := 0; i < 8; i++ {
		plain, hash, err := panAuth.NewToken(16)
		if err != nil {
			internalError(w, err)
			return
		}
		if _, err := s.db.Exec(r.Context(), `INSERT INTO recovery_codes(user_id,code_hash) VALUES($1,$2)`, a.UserID, hash); err != nil {
			internalError(w, err)
			return
		}
		codes = append(codes, plain)
	}
	if err := s.revokeOtherSessions(r, a.UserID); err != nil {
		internalError(w, err)
		return
	}
	if err := s.securityEvent(r, a.UserID, "mfa.enabled"); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"recoveryCodes": codes})
}

func (s *Server) verifySecondFactor(r *http.Request, userID uuid.UUID, code string) (bool, error) {
	var encrypted []byte
	var lastStep int64
	if err := s.db.QueryRow(r.Context(), `SELECT totp_secret,totp_last_step FROM users WHERE id=$1`, userID).Scan(&encrypted, &lastStep); err != nil {
		return false, err
	}
	if encrypted == nil {
		return true, nil
	}
	if len(code) == 22 {
		result, err := s.db.Exec(r.Context(), `DELETE FROM recovery_codes WHERE user_id=$1 AND code_hash=$2`, userID, panAuth.TokenHash(code))
		if err != nil {
			return false, err
		}
		if result.RowsAffected() == 1 {
			return true, s.securityEvent(r, userID, "mfa.recovery_used")
		}
		return false, nil
	}
	secret, err := panAuth.DecryptTOTP(s.cfg.MFAEncryptionKey, userID.String(), encrypted)
	if err != nil {
		return false, err
	}
	step, ok := panAuth.MatchTOTP(secret, code, time.Now(), lastStep)
	if !ok {
		return false, nil
	}
	result, err := s.db.Exec(r.Context(), `UPDATE users SET totp_last_step=$2 WHERE id=$1 AND totp_last_step<$2`, userID, step)
	return err == nil && result.RowsAffected() == 1, err
}

func (s *Server) revokeOtherSessions(r *http.Request, userID uuid.UUID) error {
	cookie, err := r.Cookie("pan_session")
	if err != nil {
		return err
	}
	_, err = s.db.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2`, userID, panAuth.TokenHash(cookie.Value))
	return err
}

func (s *Server) disableTOTP(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !s.requireOwnPassword(r, input.Password) {
		s.failedSecurityEvent(r, a.UserID, "mfa.disable_denied")
		writeError(w, 401, "wrong_password", "当前密码不正确")
		return
	}
	ok, err := s.verifySecondFactor(r, a.UserID, input.Code)
	if err != nil {
		internalError(w, err)
		return
	}
	if !ok {
		s.failedSecurityEvent(r, a.UserID, "mfa.disable_denied")
		writeError(w, 401, "invalid_code", "验证码或恢复码无效")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE users SET totp_secret=NULL,totp_pending_secret=NULL,totp_pending_expires=NULL,totp_last_step=-1 WHERE id=$1`, a.UserID); err != nil {
		internalError(w, err)
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM recovery_codes WHERE user_id=$1`, a.UserID); err != nil {
		internalError(w, err)
		return
	}
	if err := s.revokeOtherSessions(r, a.UserID); err != nil {
		internalError(w, err)
		return
	}
	if err := s.securityEvent(r, a.UserID, "mfa.disabled"); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
