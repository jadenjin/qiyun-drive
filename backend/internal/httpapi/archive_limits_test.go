package httpapi

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestArchiveNetworkCancellationAndBrowsingIsolation(t *testing.T) {
	s := &Server{archiveSlots: make(chan struct{}, 2)}
	stopped := make(chan struct{}, 2)
	mux := http.NewServeMux()
	mux.Handle("/archive", s.limitArchive(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("empty") == "1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		defer func() { stopped <- struct{}{} }()
		archive := zip.NewWriter(archiveWriter{r.Context(), w})
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: "stream.bin", Method: zip.Store})
		if err != nil {
			return
		}
		chunk := make([]byte, 64<<10)
		for {
			if _, err := entry.Write(chunk); err != nil {
				return
			}
			if err := archive.Flush(); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
		}
	})))
	mux.HandleFunc("/browse", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := httptest.NewServer(mux)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	var streams []*http.Response
	defer func() {
		for _, stream := range streams {
			stream.Body.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		response, err := client.Get(server.URL + "/archive")
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, response)
		if response.StatusCode != 200 {
			t.Fatalf("stream admission: %d", response.StatusCode)
		}
		if _, err := io.CopyN(io.Discard, response.Body, 1024); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range []struct {
		path   string
		status int
	}{{"/archive", 429}, {"/browse", 204}} {
		response, err := client.Get(server.URL + check.path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != check.status {
			t.Fatalf("%s: got %d", check.path, response.StatusCode)
		}
	}
	for _, stream := range streams {
		stream.Body.Close()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Fatal("disconnected ZIP kept running")
		}
	}
	// The deferred admission release may run just after the handler notification.
	deadline := time.Now().Add(time.Second)
	for len(s.archiveSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.archiveSlots) != 0 {
		t.Fatal("network cancellation leaked admission slots")
	}
	response, err := client.Get(server.URL + "/archive?empty=1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatalf("cancelled slots cannot be reused: %d", response.StatusCode)
	}
}

func TestArchiveAdmissionReleasesOnCancellation(t *testing.T) {
	s := &Server{archiveSlots: make(chan struct{}, 1)}
	entered := make(chan struct{})
	done := make(chan struct{})
	handler := s.limitArchive(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("archive has no deadline")
		}
		close(entered)
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/archive", nil).WithContext(ctx))
	}()
	<-entered
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest("GET", "/archive", nil))
	if rejected.Code != http.StatusTooManyRequests || rejected.Header().Get("Retry-After") == "" {
		t.Fatalf("admission response: %d", rejected.Code)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not release archive")
	}
	if len(s.archiveSlots) != 0 {
		t.Fatal("archive slot leaked")
	}
	next := httptest.NewRecorder()
	s.limitArchive(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(next, httptest.NewRequest("GET", "/archive", nil))
	if next.Code != http.StatusNoContent {
		t.Fatalf("slot not reusable: %d", next.Code)
	}
}

func TestArchiveWriterStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	n, err := (archiveWriter{ctx, recorder}).Write([]byte("private bytes"))
	if n != 0 || err != context.Canceled || recorder.Body.Len() != 0 {
		t.Fatal("cancelled stream wrote bytes")
	}
}
