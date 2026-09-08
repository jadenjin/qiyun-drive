package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	panAuth "pan/backend/internal/auth"
)

func authTokenHash(token string) string { return panAuth.TokenHash(token) }

// Keep unknown-user logins on the same Argon2 path as known users. The
// all-zero digest can never be a stored password hash, but it has valid,
// bounded parameters so username existence is not exposed through timing.
// #nosec G101 -- This fixed public Argon2 verifier only equalizes login timing;
// it is not assigned to an account and cannot grant authentication.
const dummyPasswordHash = "$argon2id$v=19$m=65536,t=2,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (s *Server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	var initialized bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM households)`).Scan(&initialized); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"initialized": initialized})
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	// Keep the locked check below for concurrent first-run requests, but
	// reject an already initialized installation before any password work.
	var initialized bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM households)`).Scan(&initialized); err != nil {
		internalError(w, err)
		return
	}
	if initialized {
		writeError(w, http.StatusConflict, "already_initialized", "系统已经完成初始化")
		return
	}
	var input struct {
		HouseholdName string `json:"householdName"`
		Timezone      string `json:"timezone"`
		Username      string `json:"username"`
		DisplayName   string `json:"displayName"`
		Password      string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Username = normalizeUsername(input.Username)
	input.HouseholdName = strings.TrimSpace(input.HouseholdName)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.HouseholdName == "" || len([]rune(input.HouseholdName)) > 100 || !validUsername(input.Username) || input.DisplayName == "" || len([]rune(input.DisplayName)) > 100 || len(input.Timezone) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_input", "家庭名称、用户名和显示名称不能为空")
		return
	}
	if err := validatePassword(input.Password); err != nil {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	if input.Timezone == "" {
		input.Timezone = "Asia/Shanghai"
	}
	passwordHash, err := panAuth.HashPassword(input.Password)
	if err != nil {
		internalError(w, err)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(7328462101)`); err != nil {
		internalError(w, err)
		return
	}
	var exists bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM households)`).Scan(&exists); err != nil || exists {
		if err != nil {
			internalError(w, err)
		} else {
			writeError(w, http.StatusConflict, "already_initialized", "系统已经完成初始化")
		}
		return
	}
	householdID, userID := uuid.New(), uuid.New()
	if _, err = tx.Exec(r.Context(), `INSERT INTO households(id,name,timezone) VALUES($1,$2,$3)`, householdID, input.HouseholdName, input.Timezone); err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO users(id,username,display_name,password_hash) VALUES($1,$2,$3,$4)`, userID, input.Username, input.DisplayName, passwordHash)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO household_members(household_id,user_id,role) VALUES($1,$2,'owner')`, householdID, userID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO spaces(id,household_id,owner_user_id,kind,name) VALUES($1,$2,$3,'personal',$4),($5,$2,NULL,'family',$6)`,
			uuid.New(), householdID, userID, input.DisplayName+"的空间", uuid.New(), input.HouseholdName)
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	s.createSession(w, r, userID)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len([]rune(input.Username)) > 64 || len([]rune(input.Password)) > 128 {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	var userID uuid.UUID
	passwordHash := dummyPasswordHash
	err := s.db.QueryRow(r.Context(), `SELECT id,password_hash FROM users WHERE lower(username)=$1 AND NOT disabled`, normalizeUsername(input.Username)).Scan(&userID, &passwordHash)
	passwordOK := panAuth.VerifyPassword(passwordHash, input.Password)
	if err != nil || !passwordOK {
		s.failedSecurityEvent(r, userID, "login.password_denied")
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	verified, err := s.verifySecondFactor(r, userID, input.Code)
	if err != nil {
		internalError(w, err)
		return
	}
	if !verified {
		s.failedSecurityEvent(r, userID, "login.mfa_denied")
		writeError(w, 401, "mfa_required", "请输入有效的身份验证器验证码或一次性恢复码")
		return
	}
	s.createSession(w, r, userID)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	plain, hash, err := panAuth.NewToken(32)
	if err != nil {
		internalError(w, err)
		return
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	if _, err := s.db.Exec(r.Context(), `INSERT INTO sessions(id,user_id,token_hash,expires_at,user_agent,source_ip) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), userID, hash, expires, securityAgent(r.UserAgent()), s.clientIP(r)); err != nil {
		internalError(w, err)
		return
	}
	if err := s.securityEvent(r, userID, "login.success"); err != nil {
		internalError(w, err)
		return
	}
	// #nosec G124 -- Secure is configuration-dependent because trusted private
	// LAN HTTP is supported; config validation requires it for every HTTPS URL.
	http.SetCookie(w, &http.Cookie{Name: "pan_session", Value: plain, Path: "/", Expires: expires, HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("pan_session"); err == nil {
		_, _ = s.db.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, panAuth.TokenHash(cookie.Value))
	}
	clearCookie(w, s.cfg.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{"id": a.UserID, "username": a.Username, "displayName": a.DisplayName, "role": a.Role, "householdId": a.HouseholdID})
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.DisplayName == "" || len([]rune(input.DisplayName)) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_display_name", "显示名称需为 1 到 100 个字符")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE users SET display_name=$1 WHERE id=$2`, input.DisplayName, a.UserID); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "account.profile_update", "user", &a.UserID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": a.UserID, "username": a.Username, "displayName": input.DisplayName, "role": a.Role, "householdId": a.HouseholdID})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := validatePassword(input.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	var currentHash string
	if err := s.db.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE id=$1`, a.UserID).Scan(&currentHash); err != nil {
		internalError(w, err)
		return
	}
	if !panAuth.VerifyPassword(currentHash, input.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "wrong_password", "当前密码不正确")
		return
	}
	newHash, err := panAuth.HashPassword(input.NewPassword)
	if err != nil {
		internalError(w, err)
		return
	}
	cookie, err := r.Cookie("pan_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "登录已失效")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `UPDATE users SET password_hash=$1,totp_pending_secret=NULL,totp_pending_expires=NULL WHERE id=$2`, newHash, a.UserID); err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2`, a.UserID, panAuth.TokenHash(cookie.Value))
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, a.UserID)
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "account.password_update", "user", &a.UserID, nil)
	if err := s.securityEvent(r, a.UserID, "password.changed"); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	if a.Role != "owner" && a.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", "只有家庭管理员可以邀请成员")
		return
	}
	var input struct {
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Role != "admin" {
		input.Role = "member"
	}
	if !canInviteRole(a.Role, input.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "只有家庭所有者可以邀请管理员")
		return
	}
	plain, hash, err := panAuth.NewToken(32)
	if err != nil {
		internalError(w, err)
		return
	}
	expires := time.Now().Add(7 * 24 * time.Hour)
	if _, err := s.db.Exec(r.Context(), `INSERT INTO invitations(id,household_id,created_by,token_hash,role,expires_at) VALUES($1,$2,$3,$4,$5,$6)`,
		uuid.New(), a.HouseholdID, a.UserID, hash, input.Role, expires); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "invitation.create", "invitation", nil, map[string]any{"role": input.Role})
	writeJSON(w, http.StatusCreated, map[string]any{"token": plain, "url": s.cfg.PublicBaseURL + "/invite/" + plain, "expiresAt": expires})
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token       string `json:"token"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Username = normalizeUsername(input.Username)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if !panAuth.ValidToken(input.Token, 32) || !validUsername(input.Username) || input.DisplayName == "" || len([]rune(input.DisplayName)) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_input", "用户名和显示名称不能为空")
		return
	}
	if err := validatePassword(input.Password); err != nil {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	passwordHash, err := panAuth.HashPassword(input.Password)
	if err != nil {
		internalError(w, err)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var invitationID, householdID uuid.UUID
	var role string
	err = tx.QueryRow(r.Context(), `SELECT id,household_id,role FROM invitations WHERE token_hash=$1 AND accepted_at IS NULL AND expires_at>now() FOR UPDATE`, panAuth.TokenHash(input.Token)).Scan(&invitationID, &householdID, &role)
	if errorsIsNoRows(err) {
		writeError(w, http.StatusGone, "invalid_invitation", "邀请链接无效或已过期")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	userID := uuid.New()
	if _, err = tx.Exec(r.Context(), `INSERT INTO users(id,username,display_name,password_hash) VALUES($1,$2,$3,$4)`, userID, input.Username, input.DisplayName, passwordHash); err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO household_members(household_id,user_id,role) VALUES($1,$2,$3)`, householdID, userID, role)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO spaces(id,household_id,owner_user_id,kind,name) VALUES($1,$2,$3,'personal',$4)`, uuid.New(), householdID, userID, input.DisplayName+"的空间")
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE invitations SET accepted_at=now() WHERE id=$1`, invitationID)
	}
	if err != nil {
		if isUniqueViolation(err, "users_username_lower_idx") {
			writeError(w, http.StatusConflict, "username_taken", "此用户名已被使用")
		} else {
			internalError(w, err)
		}
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	s.createSession(w, r, userID)
}

func errorsIsNoRows(err error) bool { return err == pgx.ErrNoRows }

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	rows, err := s.db.Query(r.Context(), `SELECT u.id,u.username,u.display_name,hm.role,u.created_at FROM household_members hm JOIN users u ON u.id=hm.user_id WHERE hm.household_id=$1 ORDER BY CASE hm.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END,u.created_at`, a.HouseholdID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id uuid.UUID
		var username, displayName, role string
		var createdAt time.Time
		if err := rows.Scan(&id, &username, &displayName, &role, &createdAt); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "username": username, "displayName": displayName, "role": role, "createdAt": createdAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) updateMemberRole(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	if a.Role != "owner" {
		writeError(w, http.StatusForbidden, "forbidden", "只有家庭所有者可以修改成员角色")
		return
	}
	target, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Role != "admin" && input.Role != "member" {
		writeError(w, http.StatusBadRequest, "invalid_role", "成员角色不合法")
		return
	}
	var currentRole string
	if err := s.db.QueryRow(r.Context(), `SELECT role FROM household_members WHERE household_id=$1 AND user_id=$2`, a.HouseholdID, target).Scan(&currentRole); err != nil {
		dbNotFound(w, err)
		return
	}
	if currentRole == "owner" {
		writeError(w, http.StatusBadRequest, "owner_immutable", "家庭所有者角色不能在此修改")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE household_members SET role=$1 WHERE household_id=$2 AND user_id=$3`, input.Role, a.HouseholdID, target); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "member.role_update", "user", &target, map[string]any{"from": currentRole, "to": input.Role})
	writeJSON(w, http.StatusOK, map[string]any{"id": target, "role": input.Role})
}

func (s *Server) createPasswordReset(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	if a.Role != "owner" && a.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", "只有家庭管理员可以重置密码")
		return
	}
	target, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var targetRole string
	if err := s.db.QueryRow(r.Context(), `SELECT role FROM household_members WHERE household_id=$1 AND user_id=$2`, a.HouseholdID, target).Scan(&targetRole); err != nil {
		dbNotFound(w, err)
		return
	}
	if !canResetMemberRole(a.Role, targetRole) {
		writeError(w, http.StatusForbidden, "forbidden", "只有家庭所有者可以重置所有者或管理员密码")
		return
	}
	plain, hash, err := panAuth.NewToken(32)
	if err != nil {
		internalError(w, err)
		return
	}
	expires := time.Now().Add(time.Hour)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, target); err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO password_resets(id,user_id,token_hash,expires_at) VALUES($1,$2,$3,$4)`, uuid.New(), target, hash, expires)
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "member.password_reset_create", "user", &target, nil)
	writeJSON(w, http.StatusCreated, map[string]any{"url": s.cfg.PublicBaseURL + "/reset-password/" + plain, "expiresAt": expires})
}

func (s *Server) completePasswordReset(w http.ResponseWriter, r *http.Request) {
	var input struct{ Token, Password string }
	if !decodeJSON(w, r, &input) {
		return
	}
	if !panAuth.ValidToken(input.Token, 32) {
		writeError(w, http.StatusGone, "invalid_reset", "重置链接无效或已过期")
		return
	}
	if err := validatePassword(input.Password); err != nil {
		writeError(w, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	hash, err := panAuth.HashPassword(input.Password)
	if err != nil {
		internalError(w, err)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var resetID, userID uuid.UUID
	if err := tx.QueryRow(r.Context(), `SELECT id,user_id FROM password_resets WHERE token_hash=$1 AND used_at IS NULL AND expires_at>now() FOR UPDATE`, panAuth.TokenHash(input.Token)).Scan(&resetID, &userID); err != nil {
		writeError(w, http.StatusGone, "invalid_reset", "重置链接无效或已过期")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE users SET password_hash=$1,totp_pending_secret=NULL,totp_pending_expires=NULL WHERE id=$2`, hash, userID); err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, userID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1`, userID)
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	if err := s.securityEvent(r, userID, "password.reset"); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	cookie, err := r.Cookie("pan_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "登录已失效")
		return
	}
	currentHash := panAuth.TokenHash(cookie.Value)
	rows, err := s.db.Query(r.Context(), `SELECT id,token_hash,created_at,last_seen_at,expires_at,user_agent,source_ip FROM sessions WHERE user_id=$1 AND expires_at>now() ORDER BY last_seen_at DESC`, a.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id uuid.UUID
		var hash string
		var userAgent, sourceIP string
		var createdAt, lastSeenAt, expiresAt time.Time
		if err := rows.Scan(&id, &hash, &createdAt, &lastSeenAt, &expiresAt, &userAgent, &sourceIP); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "current": hash == currentHash, "createdAt": createdAt, "lastSeenAt": lastSeenAt, "expiresAt": expiresAt, "device": deviceLabel(userAgent), "sourceIp": sourceIP, "sourceLabel": sourceLabel(sourceIP)})
	}
	if err := rows.Err(); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var revokedHash string
	if err := s.db.QueryRow(r.Context(), `DELETE FROM sessions WHERE id=$1 AND user_id=$2 RETURNING token_hash`, id, a.UserID).Scan(&revokedHash); err != nil {
		if !dbNotFound(w, err) {
			internalError(w, err)
		}
		return
	}
	if cookie, err := r.Cookie("pan_session"); err == nil && panAuth.TokenHash(cookie.Value) == revokedHash {
		clearCookie(w, s.cfg.CookieSecure)
	}
	s.audit(r.Context(), a, "account.session_revoke", "session", &id, nil)
	if err := s.securityEvent(r, a.UserID, "session.revoked"); err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func canInviteRole(actorRole, invitedRole string) bool {
	return actorRole == "owner" || (actorRole == "admin" && invitedRole == "member")
}

func canResetMemberRole(actorRole, targetRole string) bool {
	return actorRole == "owner" || (actorRole == "admin" && targetRole == "member")
}
