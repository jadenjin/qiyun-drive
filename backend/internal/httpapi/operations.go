package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Read fixed, bounded, non-secret host reports. The API receives this directory
// as read-only; it never receives a Docker socket or a host filesystem mount.
func readOpsReport(directory, name string) any {
	switch name {
	case "host-health.json", "backup-status.json", "backup-latest.json", "offhost-backup.json", "restore-status.json", "restore-latest.json":
	default:
		return nil
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil
	}
	var report map[string]any
	decoder := json.NewDecoder(io.LimitReader(file, 65537))
	if err := decoder.Decode(&report); err != nil {
		return nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil
	}
	return report
}

func operationFailure(kind, message string) string {
	value := strings.ToLower(message)
	switch {
	case strings.Contains(value, "integrity") || strings.Contains(value, "sha-256") || strings.Contains(value, "size mismatch"):
		return "文件大小或摘要不一致，请检查上传来源及存储"
	case strings.Contains(value, "permission") || strings.Contains(value, "authorization"):
		return "处理期间权限发生变化，任务已停止"
	case strings.Contains(value, "timeout") || strings.Contains(value, "deadline"):
		return "处理超时，请检查设备负载与存储连接"
	case strings.Contains(value, "nosuchkey") || strings.Contains(value, "not found") || strings.Contains(value, "404"):
		return "对象存储中找不到文件，请检查存储和备份"
	case strings.Contains(value, "space left") || strings.Contains(value, "disk full"):
		return "磁盘空间不足"
	case strings.Contains(value, "connection") || strings.Contains(value, "dial tcp"):
		return "数据库或对象存储连接失败"
	case kind == "index_photo":
		return "照片索引失败，可能是格式不支持或媒体工具处理失败"
	default:
		return "任务处理失败，请检查设备日志"
	}
}

func (s *Server) operations(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	if a.Role != "owner" {
		writeError(w, 403, "forbidden", "只有家庭所有者可以查看设备运维状态")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT kind,state,count(*),min(created_at) FROM jobs WHERE state<>'done' GROUP BY kind,state ORDER BY kind,state`)
	if err != nil {
		internalError(w, err)
		return
	}
	queue := make([]map[string]any, 0)
	for rows.Next() {
		var kind, state string
		var count int64
		var oldest time.Time
		if err := rows.Scan(&kind, &state, &count, &oldest); err != nil {
			rows.Close()
			internalError(w, err)
			return
		}
		queue = append(queue, map[string]any{"kind": kind, "state": state, "count": count, "oldestAt": oldest})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internalError(w, err)
		return
	}
	rows, err = s.db.Query(r.Context(), `SELECT kind,attempts,last_error,updated_at FROM jobs WHERE state='failed' ORDER BY updated_at DESC LIMIT 20`)
	if err != nil {
		internalError(w, err)
		return
	}
	failures := make([]map[string]any, 0)
	for rows.Next() {
		var kind string
		var attempts int
		var message *string
		var updated time.Time
		if err := rows.Scan(&kind, &attempts, &message, &updated); err != nil {
			rows.Close()
			internalError(w, err)
			return
		}
		detail := ""
		if message != nil {
			detail = *message
		}
		failures = append(failures, map[string]any{"kind": kind, "attempts": attempts, "reason": operationFailure(kind, detail), "updatedAt": updated})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		internalError(w, err)
		return
	}
	var heartbeat *time.Time
	if err := s.db.QueryRow(r.Context(), `SELECT max(updated_at) FROM runtime_health WHERE component='worker'`).Scan(&heartbeat); err != nil {
		internalError(w, err)
		return
	}
	var pendingIntegrity int64
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM assets WHERE status='ready' AND sha256 IS NULL`).Scan(&pendingIntegrity); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"queue": queue, "failures": failures, "workerHeartbeat": heartbeat, "pendingIntegrity": pendingIntegrity, "host": readOpsReport(s.cfg.OpsDirectory, "host-health.json"), "backup": readOpsReport(s.cfg.OpsDirectory, "backup-status.json"), "latestBackup": readOpsReport(s.cfg.OpsDirectory, "backup-latest.json"), "offhost": readOpsReport(s.cfg.OpsDirectory, "offhost-backup.json"), "restore": readOpsReport(s.cfg.OpsDirectory, "restore-status.json"), "latestRestore": readOpsReport(s.cfg.OpsDirectory, "restore-latest.json")})
}
