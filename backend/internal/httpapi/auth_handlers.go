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

func (s *Server) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	var initialized bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM households)`).Scan(&initialized); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"initialized": initialized})
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
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
	if input.HouseholdName == "" || len([]rune(input.HouseholdName)) > 100 || input.Username == "" || len([]rune(input.Username)) > 64 || input.DisplayName == "" || len([]rune(input.DisplayName)) > 100 || len(input.Timezone) > 100 {
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
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len([]rune(input.Username)) > 64 || len([]rune(input.Password)) > 128 {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	var userID uuid.UUID
	var passwordHash string
	err := s.db.QueryRow(r.Context(), `SELECT id,password_hash FROM users WHERE lower(username)=$1 AND NOT disabled`, normalizeUsername(input.Username)).Scan(&userID, &passwordHash)
	if err != nil || !panAuth.VerifyPassword(passwordHash, input.Password) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
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
	if _, err := s.db.Exec(r.Context(), `INSERT INTO sessions(id,user_id,token_hash,expires_at) VALUES($1,$2,$3,$4)`, uuid.New(), userID, hash, expires); err != nil {
		internalError(w, err)
		return
	}
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
	if _, err = tx.Exec(r.Context(), `UPDATE users SET password_hash=$1 WHERE id=$2`, newHash, a.UserID); err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2`, a.UserID, panAuth.TokenHash(cookie.Value))
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
	if len(input.Token) > 256 || input.Username == "" || len([]rune(input.Username)) > 64 || input.DisplayName == "" || len([]rune(input.DisplayName)) > 100 {
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
		internalError(w, err)
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
	if targetRole == "owner" && a.Role != "owner" {
		writeError(w, http.StatusForbidden, "forbidden", "不能重置所有者密码")
		return
	}
	plain, hash, err := panAuth.NewToken(32)
	if err != nil {
		internalError(w, err)
		return
	}
	expires := time.Now().Add(time.Hour)
	_, err = s.db.Exec(r.Context(), `INSERT INTO password_resets(id,user_id,token_hash,expires_at) VALUES($1,$2,$3,$4)`, uuid.New(), target, hash, expires)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"url": s.cfg.PublicBaseURL + "/reset-password/" + plain, "expiresAt": expires})
}

func (s *Server) completePasswordReset(w http.ResponseWriter, r *http.Request) {
	var input struct{ Token, Password string }
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Token) > 256 {
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
	if _, err := tx.Exec(r.Context(), `UPDATE users SET password_hash=$1 WHERE id=$2`, hash, userID); err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE password_resets SET used_at=now() WHERE id=$1`, resetID)
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
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
