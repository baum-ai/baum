//go:build ignore

// Exercise the installed desktop support binary without an account or agents.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: daemon-smoke.go BINARY")
		os.Exit(1)
	}
	if err := smoke(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "daemon smoke:", err)
		os.Exit(1)
	}
	fmt.Println("desktop daemon smoke passed")
}

func smoke(binary string) (runErr error) {
	binary, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	tempBase := ""
	if os.PathSeparator == '/' {
		tempBase = "/tmp"
	}
	work, err := os.MkdirTemp(tempBase, "baum-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "BAUM_") || strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GITHUB_") || strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "CODEX_") || key == "HOME" || key == "USERPROFILE" || key == "APPDATA" || key == "LOCALAPPDATA" || key == "SHELL" || strings.HasPrefix(key, "XDG_") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "HOME="+work, "USERPROFILE="+work, "APPDATA="+filepath.Join(work, "appdata"), "LOCALAPPDATA="+filepath.Join(work, "localappdata"), "XDG_STATE_HOME="+filepath.Join(work, "state"), "XDG_DATA_HOME="+filepath.Join(work, "data"), "XDG_CONFIG_HOME="+filepath.Join(work, "config"), "XDG_CACHE_HOME="+filepath.Join(work, "cache"), "XDG_RUNTIME_DIR="+filepath.Join(work, "run"), "CI=1")
	if os.PathSeparator == '/' {
		env = append(env, "SHELL=/bin/sh")
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = env
		cmd.Dir = work
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, stderr.String())
		}
		return out, nil
	}
	if _, err := run("daemon", "status", "--json"); err == nil {
		return fmt.Errorf("isolated daemon already runs before smoke test")
	}
	out, err := run("daemon", "ensure", "--json")
	if err != nil {
		return err
	}
	var status struct {
		PID    int    `json:"pid"`
		State  string `json:"state"`
		App    string `json:"app"`
		Socket string `json:"socket"`
	}
	if err = json.Unmarshal(out, &status); err != nil {
		return err
	}
	if status.PID <= 0 || status.State != filepath.Join(work, "state", "baum", "daemon") || status.Socket != filepath.Join(work, "run", "baum", "daemon.sock") {
		return fmt.Errorf("daemon did not use isolated state and socket")
	}
	defer func() {
		if _, err := run("daemon", "stop"); err != nil {
			if process, e := os.FindProcess(status.PID); e == nil {
				_ = process.Kill()
			}
			if runErr == nil {
				runErr = err
			}
		}
	}()
	raw, err := os.ReadFile(filepath.Join(status.State, "credential.json"))
	if err != nil {
		return err
	}
	var credential struct {
		URL string `json:"url"`
	}
	if err = json.Unmarshal(raw, &credential); err != nil {
		return err
	}
	launch, err := url.Parse(credential.URL)
	if err != nil {
		return err
	}
	token := strings.TrimPrefix(launch.Fragment, "token=")
	if token == "" || launch.Scheme+"://"+launch.Host != status.App {
		return fmt.Errorf("invalid launch credential")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := client.Get(status.App + "/api/health")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("unauthenticated health returned %d", resp.StatusCode)
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	req, _ := http.NewRequest("POST", status.App+"/api/auth/exchange", bytes.NewReader(body))
	req.Header.Set("Origin", status.App)
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		return err
	}
	var exchange struct {
		Path string `json:"path"`
	}
	err = json.NewDecoder(resp.Body).Decode(&exchange)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil || !strings.HasPrefix(exchange.Path, "/s/") {
		return fmt.Errorf("launch exchange failed")
	}
	fetch := func(path string) ([]byte, error) {
		resp, err := client.Get(status.App + path)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("app resource returned %d", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	}
	health, err := fetch(exchange.Path + "api/health")
	if err != nil {
		return err
	}
	if !bytes.Contains(health, []byte(`"status":"ok"`)) {
		return fmt.Errorf("invalid health response")
	}
	html, err := fetch(exchange.Path)
	if err != nil {
		return err
	}
	if !bytes.Contains(html, []byte(`<div id="root">`)) {
		return fmt.Errorf("embedded app is missing")
	}
	match := regexp.MustCompile(`(?:src|href)="(?:\./)?(assets/[^" ]+\.(?:js|css))"`).FindSubmatch(html)
	if len(match) < 2 {
		return fmt.Errorf("embedded app has no asset reference")
	}
	asset, err := fetch(exchange.Path + string(match[1]))
	if err != nil {
		return err
	}
	if len(asset) == 0 {
		return fmt.Errorf("embedded asset is empty")
	}
	if _, err = run("daemon", "status", "--json"); err != nil {
		return err
	}
	if _, err = run("daemon", "stop"); err != nil {
		return err
	}
	if _, err = run("daemon", "status", "--json"); err == nil {
		return fmt.Errorf("stopped daemon still answers")
	}
	return nil
}
