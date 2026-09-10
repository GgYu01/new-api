package lifecycle

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const pingBytes = ": PING\n\n"

// Writer is the request-owned arbiter for downstream bytes. Heartbeat, payload,
// and terminal writes serialize on writeMu. Header map mutations also take
// writeMu so concurrent Header()/Write()/Flush() cannot race Gin's map.
type Writer struct {
	gin.ResponseWriter
	inner http.ResponseWriter
	lr    *LogicalRequest

	writeMu sync.Mutex

	status     int
	size       int
	headerSent bool
}

func newWriter(c *gin.Context, lr *LogicalRequest) *Writer {
	inner := c.Writer
	w := &Writer{
		ResponseWriter: inner,
		inner:          inner,
		lr:             lr,
		status:         http.StatusOK,
		size:           -1,
	}
	return w
}

func (w *Writer) lock() {
	w.writeMu.Lock()
}

func (w *Writer) unlock() {
	w.writeMu.Unlock()
}

func (w *Writer) Header() http.Header {
	// Header map mutations still serialize with Write/Flush via writeMu at
	// the call sites that copy values. Returning the live map is required by
	// net/http; callers that mutate it concurrently with Write still take
	// writeMu in PrepareSSEHeaders / WriteKeepalive / WriteHeader.
	w.lock()
	defer w.unlock()
	return w.ResponseWriter.Header()
}

func (w *Writer) WriteHeader(code int) {
	w.lock()
	defer w.unlock()
	w.writeHeaderLocked(code)
}

func (w *Writer) writeHeaderLocked(code int) {
	if w.headerSent {
		return
	}
	if code > 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.headerSent = true
	if w.size < 0 {
		w.size = 0
	}
	if w.lr != nil {
		w.lr.MarkHeadersCommitted()
	}
}

func (w *Writer) WriteHeaderNow() {
	w.lock()
	defer w.unlock()
	if !w.headerSent {
		w.writeHeaderLocked(w.status)
	}
}

func (w *Writer) Write(data []byte) (int, error) {
	w.lock()
	defer w.unlock()
	return w.writeLocked(data)
}

func (w *Writer) writeLocked(data []byte) (int, error) {
	if !w.headerSent {
		w.writeHeaderLocked(w.status)
	}
	n, err := w.ResponseWriter.Write(data)
	if w.size < 0 {
		w.size = 0
	}
	w.size += n
	if w.lr != nil && n > 0 {
		classifyWrite(w.lr, data[:n])
	}
	return n, err
}

func (w *Writer) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *Writer) Status() int {
	w.lock()
	defer w.unlock()
	return w.status
}

func (w *Writer) Size() int {
	w.lock()
	defer w.unlock()
	if w.size < 0 {
		return 0
	}
	return w.size
}

func (w *Writer) Written() bool {
	w.lock()
	defer w.unlock()
	return w.headerSent || w.size >= 0
}

func (w *Writer) Flush() {
	w.lock()
	defer w.unlock()
	if !w.headerSent {
		w.writeHeaderLocked(w.status)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	if w.lr != nil {
		w.lr.MarkHeadersCommitted()
	}
}

func (w *Writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.lock()
	defer w.unlock()
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (w *Writer) CloseNotify() <-chan bool {
	if cn, ok := w.ResponseWriter.(http.CloseNotifier); ok {
		return cn.CloseNotify()
	}
	ch := make(chan bool, 1)
	return ch
}

func (w *Writer) Pusher() http.Pusher {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p
	}
	return nil
}

func (w *Writer) Unwrap() http.ResponseWriter {
	if u, ok := w.ResponseWriter.(interface{ Unwrap() http.ResponseWriter }); ok {
		return u.Unwrap()
	}
	return w.inner
}

func (w *Writer) extendDeadlineLocked() {
	timeout := WriteNoProgressTimeout()
	if timeout <= 0 {
		return
	}
	rc := http.NewResponseController(w.ResponseWriter)
	_ = rc.SetWriteDeadline(time.Now().Add(timeout))
}

func (w *Writer) WriteKeepalive() error {
	w.lock()
	defer w.unlock()
	w.extendDeadlineLocked()
	if !w.headerSent {
		if w.HeaderUnlockedContentType() == "" {
			w.ResponseWriter.Header().Set("Content-Type", "text/event-stream")
			w.ResponseWriter.Header().Set("Cache-Control", "no-cache")
			w.ResponseWriter.Header().Set("Connection", "keep-alive")
			w.ResponseWriter.Header().Set("X-Accel-Buffering", "no")
		}
		w.writeHeaderLocked(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write([]byte(pingBytes))
	if w.size < 0 {
		w.size = 0
	}
	w.size += n
	if w.lr != nil {
		w.lr.MarkKeepaliveOnly()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	return err
}

// HeaderUnlockedContentType is called with writeMu held.
func (w *Writer) HeaderUnlockedContentType() string {
	return w.ResponseWriter.Header().Get("Content-Type")
}

func (w *Writer) WritePayload(p []byte) (int, error) {
	w.lock()
	defer w.unlock()
	w.extendDeadlineLocked()
	return w.writeLocked(p)
}

func (w *Writer) WriteTerminal(p []byte) (int, error) {
	w.lock()
	defer w.unlock()
	w.extendDeadlineLocked()
	n, err := w.writeLocked(p)
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	if w.lr != nil {
		w.lr.MarkTerminalSent()
	}
	return n, err
}

func (w *Writer) PrepareSSEHeaders() {
	w.lock()
	defer w.unlock()
	if w.headerSent {
		return
	}
	h := w.ResponseWriter.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("Transfer-Encoding", "chunked")
	h.Set("X-Accel-Buffering", "no")
}

var (
	_ gin.ResponseWriter = (*Writer)(nil)
	_ http.Flusher       = (*Writer)(nil)
	_ io.Writer          = (*Writer)(nil)
)
