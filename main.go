package main

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
)

//go:embed style.css
var styleFS embed.FS

var (
	md goldmark.Markdown

	filePath string
	content  []byte
	mu       sync.RWMutex

	clients   = make(map[chan struct{}]struct{})
	clientsMu sync.Mutex
)

func init() {
	md = goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.TaskList,
			highlighting.NewHighlighting(
				highlighting.WithStyle("github"),
			),
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithUnsafe(),
		),
	)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mdview: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Parse args
	args := os.Args[1:]
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Fprintf(os.Stderr, "Usage: mdview <file.md> [file2.md ...]\n")
			fmt.Fprintf(os.Stderr, "       cat file.md | mdview\n\n")
			fmt.Fprintf(os.Stderr, "Renders Markdown in a browser with live reload.\n")
			fmt.Fprintf(os.Stderr, "Close the browser tab or press Ctrl+C to exit.\n")
			os.Exit(0)
		}
	}
	if len(args) == 0 {
		// Check for stdin pipe
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			mu.Lock()
			content = data
			filePath = ""
			mu.Unlock()
		} else {
			fmt.Fprintf(os.Stderr, "Usage: mdview <file.md> [file2.md ...]\n")
			fmt.Fprintf(os.Stderr, "       cat file.md | mdview\n")
			os.Exit(1)
		}
	} else {
		// Read first file (support multiple later via concatenation)
		var combined []byte
		for _, arg := range args {
			data, err := os.ReadFile(arg)
			if err != nil {
				return fmt.Errorf("reading %s: %w", arg, err)
			}
			if len(combined) > 0 {
				combined = append(combined, '\n', '\n')
			}
			combined = append(combined, data...)
		}
		mu.Lock()
		filePath = args[0]
		content = combined
		mu.Unlock()
	}

	// Start server on random port
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return fmt.Errorf("starting server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://localhost:%d", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handlePage)
	mux.HandleFunc("/events", handleSSE)
	mux.HandleFunc("/raw", handleRaw)

	server := &http.Server{Handler: mux}

	// Graceful shutdown context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start server
	go func() {
		if err := server.Serve(listener); err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			cancel()
		}
	}()

	fmt.Fprintf(os.Stderr, "Serving at %s\n", url)

	// Open browser
	if err := openBrowser(url); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open browser: %v\nOpen %s manually.\n", err, url)
	}

	// File watcher (poll-based, no external dependency)
	if filePath != "" {
		go watchFiles(ctx, args)
	}

	// Wait for shutdown signal or all clients disconnecting
	disconnectCh := make(chan struct{})
	go func() {
		// Wait for at least one client to connect, then watch for all disconnects
		for {
			time.Sleep(500 * time.Millisecond)
			clientsMu.Lock()
			count := len(clients)
			clientsMu.Unlock()
			if count > 0 {
				break
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		// Now wait for all clients to disconnect
		for {
			time.Sleep(500 * time.Millisecond)
			clientsMu.Lock()
			count := len(clients)
			clientsMu.Unlock()
			if count == 0 {
				close(disconnectCh)
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()

	select {
	case <-sigCh:
		fmt.Fprintf(os.Stderr, "\nShutting down...\n")
	case <-disconnectCh:
		fmt.Fprintf(os.Stderr, "Browser disconnected, shutting down...\n")
	case <-ctx.Done():
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	return server.Shutdown(shutdownCtx)
}

func renderMarkdown() ([]byte, error) {
	mu.RLock()
	src := content
	mu.RUnlock()

	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	rendered, err := renderMarkdown()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	css, _ := styleFS.ReadFile("style.css")

	title := "mdview"
	if filePath != "" {
		title = filepath.Base(filePath) + " — mdview"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>%s</style>
</head>
<body>
<button class="theme-toggle" id="themeToggle" title="Toggle dark/light mode">🌓</button>
<div class="container">
%s
</div>
<script>
(function() {
  // Theme toggle
  const toggle = document.getElementById('themeToggle');
  const root = document.documentElement;
  const stored = localStorage.getItem('mdview-theme');
  if (stored) root.setAttribute('data-theme', stored);

  toggle.addEventListener('click', function() {
    const current = root.getAttribute('data-theme');
    let next;
    if (current === 'dark') next = 'light';
    else if (current === 'light') next = '';
    else next = 'dark';

    if (next) {
      root.setAttribute('data-theme', next);
      localStorage.setItem('mdview-theme', next);
    } else {
      root.removeAttribute('data-theme');
      localStorage.removeItem('mdview-theme');
    }
  });

  // SSE live reload
  const evtSource = new EventSource('/events');
  evtSource.addEventListener('reload', function() {
    fetch('/raw').then(r => r.text()).then(html => {
      document.querySelector('.container').innerHTML = html;
    });
  });
  evtSource.onerror = function() {
    // Server went away, stop retrying
    evtSource.close();
  };
})();
</script>
</body>
</html>`, title, string(css), string(rendered))
}

func handleRaw(w http.ResponseWriter, r *http.Request) {
	rendered, err := renderMarkdown()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(rendered)
}

func handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan struct{}, 1)
	clientsMu.Lock()
	clients[ch] = struct{}{}
	clientsMu.Unlock()

	defer func() {
		clientsMu.Lock()
		delete(clients, ch)
		clientsMu.Unlock()
	}()

	// Send initial ping
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-ch:
			fmt.Fprintf(w, "event: reload\ndata: reload\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func notifyClients() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	for ch := range clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func watchFiles(ctx context.Context, paths []string) {
	modTimes := make(map[string]time.Time)
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		modTimes[abs] = info.ModTime()
	}

	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			changed := false
			for absPath, lastMod := range modTimes {
				info, err := os.Stat(absPath)
				if err != nil {
					continue
				}
				if info.ModTime().After(lastMod) {
					modTimes[absPath] = info.ModTime()
					changed = true
				}
			}
			if changed {
				// Re-read all files
				var combined []byte
				for _, p := range paths {
					data, err := os.ReadFile(p)
					if err != nil {
						continue
					}
					if len(combined) > 0 {
						combined = append(combined, '\n', '\n')
					}
					combined = append(combined, data...)
				}
				mu.Lock()
				content = combined
				mu.Unlock()
				notifyClients()
			}
		}
	}
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start", strings.ReplaceAll(url, "&", "^&")}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return exec.Command(cmd, args...).Start()
}
