package httpapi

import (
	"context"
	"io"
	"net/http"
	"time"
)

type archiveWriter struct {
	ctx context.Context
	w   io.Writer
}

func (w archiveWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.w.Write(p)
}

// Archive work has its own small budget so compression and slow downloads
// cannot consume every connection available to ordinary browsing.
func (s *Server) limitArchive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.archiveSlots <- struct{}{}:
			defer func() { <-s.archiveSlots }()
		default:
			w.Header().Set("Retry-After", "10")
			writeError(w, http.StatusTooManyRequests, "archive_busy", "正在处理其他打包下载，请稍后重试")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		controller := http.NewResponseController(w)
		deadline, _ := ctx.Deadline()
		_ = controller.SetWriteDeadline(deadline)
		defer controller.SetWriteDeadline(time.Time{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
