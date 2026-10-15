package uiserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Instance holds the per-user process lock for as long as its server lives.
// SQLite's OS locking also releases it after an abrupt process exit.
type Instance struct {
	Server   *Server
	URL      string
	Existing bool
	conn     *sql.Conn
	db       *sql.DB
	path     string
}

func StateDirectory() (string, error) {
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(runtime) {
		return filepath.Join(runtime, "baum"), nil
	}
	state := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "baum"), nil
}

// Start either owns a new instance or returns the authenticated launch URL of
// the running one. Directory must be private to the current user.
func Start(ctx context.Context, directory string, port int, opts Options) (*Instance, error) {
	if directory == "" {
		var err error
		directory, err = StateDirectory()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("ui instance directory must be a private directory (0700)")
	}
	lockpath := filepath.Join(directory, "ui.lock.sqlite")
	f, err := os.OpenFile(lockpath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	info, err = os.Lstat(lockpath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("ui lock file must be private (0600)")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(lockpath)}).String()+"?_pragma=busy_timeout(0)")
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	path := filepath.Join(directory, "ui-instance.json")
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		if err == nil {
			break
		}
		// Only lock contention can mean another application instance. Corrupt or
		// inaccessible databases must not hide behind an unrelated metadata file.
		var code interface{ Code() int }
		if !errors.As(err, &code) || (code.Code()&255 != 5 && code.Code()&255 != 6) {
			_ = conn.Close()
			_ = db.Close()
			return nil, fmt.Errorf("lock ui instance: %w", err)
		}
		if launch, ok := running(ctx, path); ok {
			_ = conn.Close()
			_ = db.Close()
			return &Instance{URL: launch, Existing: true}, nil
		}
		select {
		case <-ctx.Done():
			_ = conn.Close()
			_ = db.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			_ = conn.Close()
			_ = db.Close()
			return nil, errors.New("another ui instance holds the lock but is not responding; retry after it starts or stops")
		case <-tick.C:
		}
	}
	instance := &Instance{conn: conn, db: db, path: path}
	server, err := New(opts)
	if err != nil {
		_ = instance.Close()
		return nil, err
	}
	launch, err := server.Listen(port)
	if err != nil {
		_ = instance.Close()
		return nil, err
	}
	instance.Server = server
	instance.URL = launch
	data, _ := json.Marshal(struct {
		URL string `json:"url"`
	}{launch})
	// Rename only while holding the process lock; cleanup keeps that lock until
	// after removing metadata, so it cannot remove a successor's capability.
	temporary, err := os.CreateTemp(directory, ".ui-instance-*")
	if err == nil {
		_, err = temporary.Write(data)
		err = errors.Join(err, temporary.Close())
		if err == nil {
			err = os.Rename(temporary.Name(), path)
		}
		_ = os.Remove(temporary.Name())
	}
	if err != nil {
		_ = server.listener.Close()
		_ = instance.Close()
		return nil, err
	}
	return instance, nil
}
func running(ctx context.Context, path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var stored struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(data, &stored) != nil {
		return "", false
	}
	u, err := url.Parse(stored.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "/" || u.RawQuery != "" {
		return "", false
	}
	fragment, err := url.ParseQuery(u.Fragment)
	token := fragment.Get("token")
	if err != nil || len(token) != 43 || len(fragment) != 1 {
		return "", false
	}
	u.Fragment = ""
	u.Path = "/api/health"
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 300 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 128))
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(body), `"status":"ok"`) {
		return "", false
	}
	return stored.URL, true
}
func (i *Instance) Close() error {
	if i.conn == nil {
		return nil
	}
	if i.Server != nil && i.Server.listener != nil {
		_ = i.Server.listener.Close()
	}
	remove := os.Remove(i.path)
	if errors.Is(remove, os.ErrNotExist) {
		remove = nil
	}
	_, rollback := i.conn.ExecContext(context.Background(), "ROLLBACK")
	err := errors.Join(remove, rollback, i.conn.Close(), i.db.Close())
	i.conn = nil
	return err
}
