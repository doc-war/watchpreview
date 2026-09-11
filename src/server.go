package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// 控制端点前缀固定为常量，不再对外暴露成参数：
// 本地开发工具独占端口，不存在多个 control 端点在同一路径下
// 相互冲突需要命名空间隔离的场景。
const (
	reloadEndpointPath = "/_watchpreview/reload"
	stopEndpointPath   = "/_watchpreview/control/stop"
	statusEndpointPath = "/_watchpreview/control/status"
)

type Preview struct {
	root  string
	id    string
	token string

	reload *ReloadHub
	server *http.Server

	done     chan struct{}
	doneOnce sync.Once
}

func NewPreview(root, id, token string, hub *ReloadHub) *Preview {
	return &Preview{
		root:   root,
		id:     id,
		token:  token,
		reload: hub,
		done:   make(chan struct{}),
	}
}

func (p *Preview) Done() <-chan struct{} { return p.done }

func (p *Preview) RequestStop() {
	p.doneOnce.Do(func() { close(p.done) })
}

func (p *Preview) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(reloadEndpointPath, p.handleReload)
	mux.HandleFunc(stopEndpointPath, p.handleStop)
	mux.HandleFunc(statusEndpointPath, p.handleStatus)
	mux.HandleFunc("/", p.handleStatic)
	return mux
}

func (p *Preview) handleReload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE unsupported", http.StatusInternalServerError)
		return
	}

	ch := p.reload.Subscribe()
	defer p.reload.Unsubscribe(ch)

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, "data: reload\n\n")
			flusher.Flush()
		}
	}
}

func (p *Preview) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := r.Header.Get("X-WatchPreview-Token")
	if token == "" || token != p.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	p.RequestStop()
}

// handleStatus 不需要 token：只暴露 id/root，供调用方核对
// "这个端口上跑的确实是我认为的那个实例"，是降级容错逻辑的关键依据。
func (p *Preview) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%q,"root":%q}`, p.id, p.root)
}

func (p *Preview) handleStatic(w http.ResponseWriter, r *http.Request) {
	requestPath := filepath.Clean(r.URL.Path)
	if requestPath == "/" {
		requestPath = "/index.html"
	}

	relative := strings.TrimPrefix(requestPath, "/")
	fullPath := filepath.Join(p.root, filepath.FromSlash(relative))

	rootAbs, err := filepath.Abs(p.root)
	if err != nil {
		http.Error(w, "invalid root", http.StatusInternalServerError)
		return
	}

	fileAbs, err := filepath.Abs(fullPath)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	rel, err := filepath.Rel(rootAbs, fileAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	resolved, ok := p.resolveFile(fileAbs)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if strings.HasSuffix(strings.ToLower(resolved), ".html") {
		p.serveHTML(w, resolved)
		return
	}

	http.ServeFile(w, r, resolved)
}

func (p *Preview) resolveFile(fileAbs string) (string, bool) {
	info, err := os.Stat(fileAbs)

	if err == nil && info.IsDir() {
		indexPath := filepath.Join(fileAbs, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			return indexPath, true
		}
	} else if err == nil {
		return fileAbs, true
	}

	return "", false
}

func (p *Preview) serveHTML(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}

	reloadScript := fmt.Sprintf(`<script>
(() => {
    const connect = () => {
        const source = new EventSource(%q);
        source.onmessage = () => location.reload();
        source.onerror = () => {
            source.close();
            setTimeout(connect, 1000);
        };
    };
    connect();
})();
</script>`, reloadEndpointPath)

	content := string(data)
	idx := strings.Index(strings.ToLower(content), "</head>")

	if idx >= 0 {
		content = content[:idx] + reloadScript + content[idx:]
	} else {
		content += reloadScript
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, content)
}
