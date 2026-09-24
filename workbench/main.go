// Mirasim's local desktop companion. The upstream gateway remains a separate,
// unmodified process; closing this window does not interrupt API clients.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const version = "1.0.2"

//go:embed web/index.html web/styles.css web/app.js web/protocol.mjs web/quota.mjs web/mark.svg
var assets embed.FS

type app struct {
	repo, addr, gateway, password string
	client                        *http.Client
	serviceMu                     sync.Mutex
	sessionMu                     sync.Mutex
	cookie                        *http.Cookie
	cookieUntil                   time.Time
	stateMu                       sync.Mutex
	lastError                     string
	shutdown                      func()
}

func main() {
	repoFlag := flag.String("repo", "", "Deployment directory")
	addr := flag.String("addr", "127.0.0.1:7901", "Loopback workbench address")
	port := flag.Int("gateway-port", 8787, "Gateway port")
	window := flag.Bool("window", true, "Open native desktop window")
	open := flag.Bool("open", true, "Open browser in service mode")
	auto := flag.Bool("autostart", true, "Start gateway automatically")
	flag.Parse()
	exe, _ := os.Executable()
	repo := *repoFlag
	if repo == "" {
		repo = filepath.Dir(exe)
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		log.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(repo, "logs"), 0700); err != nil {
		log.Fatal(err)
	}
	lf, err := os.OpenFile(filepath.Join(repo, "logs", "workbench.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatal(err)
	}
	defer lf.Close()
	log.SetOutput(lf)
	if err = validateAddr(*addr); err != nil {
		log.Fatal(err)
	}
	if *port < 1024 || *port > 65535 {
		log.Fatal("gateway port must be 1024–65535")
	}
	url := "http://" + *addr
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		// Only reuse a positively identified workbench, never an arbitrary listener.
		c := &http.Client{Timeout: time.Second}
		r, e := c.Get(url + "/identity")
		if e != nil {
			log.Fatal(err)
		}
		defer r.Body.Close()
		var id map[string]string
		if json.NewDecoder(r.Body).Decode(&id) != nil || id["app"] != "mirasim-workbench" || !strings.EqualFold(id["repo"], repo) {
			log.Fatal("workbench port is occupied by another application")
		}
		if *window {
			showWindow(repo, url)
		} else if *open {
			_ = openExternal(url)
		}
		return
	}
	defer ln.Close()
	if err = secureDataDir(filepath.Join(repo, "data")); err != nil {
		log.Fatal(err)
	}
	password, err := loadPassword(repo)
	if err != nil {
		log.Fatal(err)
	}
	a := &app{repo: repo, addr: *addr, gateway: fmt.Sprintf("http://127.0.0.1:%d", *port), password: password,
		client: &http.Client{Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	srv := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	var once sync.Once
	a.shutdown = func() { once.Do(func() { close(done) }) }
	go func() {
		if e := srv.Serve(ln); e != nil && !errors.Is(e, http.ErrServerClosed) {
			log.Print(e)
			a.shutdown()
		}
	}()
	if *auto {
		go func() { a.setError(a.startGateway()) }()
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	defer signal.Stop(stop)
	go func() {
		select {
		case <-stop:
			a.shutdown()
		case <-done:
		}
	}()
	log.Printf("Mirasim workbench %s at %s", version, url)
	if *window {
		showWindowUntil(repo, url, done)
		a.shutdown()
	} else {
		if *open {
			_ = openExternal(url)
		}
		<-done
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func showWindow(repo, url string) {
	showWindowUntil(repo, url, nil)
}
func showWindowUntil(repo, url string, done <-chan struct{}) {
	err := runAppWindow(windowOptions{URL: url, Title: "Mirasim 本地工作台", IconPath: filepath.Join(repo, "workbench.ico"), DataDir: filepath.Join(repo, "data", "webview2"), Width: 1220, Height: 820, Done: done})
	if err != nil {
		log.Printf("desktop: %v", err)
		_ = openExternal(url)
		if done != nil {
			<-done
		}
	}
}

func validateAddr(addr string) error {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(h)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("工作台只能监听本机回环地址")
	}
	return nil
}

func loadPassword(repo string) (string, error) {
	p := filepath.Join(repo, "data", "workbench-secret.dpapi")
	if b, err := os.ReadFile(p); err == nil {
		plain, err := unprotect(b)
		return string(plain), err
	} else if !os.IsNotExist(err) {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	plain := hex.EncodeToString(b)
	cipher, err := protect([]byte(plain))
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return "", err
	}
	return plain, os.WriteFile(p, cipher, 0600)
}

func (a *app) setError(err error) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	a.lastError = ""
	if err != nil {
		a.lastError = err.Error()
		log.Print(err)
	}
}

func jsonReply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, err error) {
	jsonReply(w, code, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func (a *app) routes() http.Handler {
	// Windows registry associations may otherwise serve .mjs as text/plain.
	_ = mime.AddExtensionType(".mjs", "application/javascript")
	_ = mime.AddExtensionType(".js", "application/javascript")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /identity", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, 200, map[string]string{"app": "mirasim-workbench", "version": version, "repo": a.repo})
	})
	mux.HandleFunc("GET /api/state", a.handleState)
	mux.HandleFunc("POST /api/service/{action}", a.handleService)
	mux.HandleFunc("GET /api/logs", a.handleLogs)
	mux.HandleFunc("POST /api/reveal", a.handleReveal)
	mux.HandleFunc("POST /api/external", a.handleExternal)
	mux.HandleFunc("POST /api/desktop", a.handleDesktop)
	for _, method := range []string{"GET", "POST", "PATCH", "PUT", "DELETE"} {
		mux.HandleFunc(method+" /api/admin/", a.handleAdmin)
	}
	mux.HandleFunc("POST /api/request", a.handleRequest)
	sub, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		// Host validation prevents DNS rebinding; the custom header plus strict
		// same-origin checks prevents websites from operating the local service.
		if r.Host != a.addr {
			fail(w, 403, errors.New("非本机工作台请求"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			origin := r.Header.Get("Origin")
			if r.Header.Get("X-Mir-Workbench") != "1" || (origin != "" && origin != "http://"+a.addr) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, 403, errors.New("请通过本地工作台操作"))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func readTail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "暂无日志"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "无法读取日志"
	}
	if st.Size() > 64<<10 {
		_, _ = f.Seek(-(64 << 10), io.SeekEnd)
	}
	b, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	return string(b)
}
