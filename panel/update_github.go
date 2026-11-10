package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 更新检查：只查 GitHub Releases，不自动下载/替换正在运行的程序。
// 界面拿到结果后展示「当前版本 → 最新版本」并给一个前往 Release 页的按钮，
// 用户自己下载新包覆盖即可（exe 正在运行，自己替换自己必然失败）。

type releaseAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

type githubRelease struct {
	Tag         string         `json:"tag_name"`
	Name        string         `json:"name"`
	Body        string         `json:"body"`
	PageURL     string         `json:"html_url"`
	PublishedAt string         `json:"published_at"`
	Prerelease  bool           `json:"prerelease"`
	Draft       bool           `json:"draft"`
	Assets      []releaseAsset `json:"assets"`
}

type updateResult struct {
	OK          bool   `json:"ok"`
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	HasUpdate   bool   `json:"hasUpdate"`
	Tag         string `json:"tag"`
	Name        string `json:"name"`
	PublishedAt string `json:"publishedAt"`
	ReleaseURL  string `json:"releaseURL"`
	DownloadURL string `json:"downloadURL"`
	Size        int64  `json:"downloadSize"`
	Notes       string `json:"notes"`
	CheckedAt   string `json:"checkedAt"`
	Error       string `json:"error"`
	Repo        string `json:"repo"`
}

var (
	updateMu       sync.Mutex
	updateCache    *updateResult
	updateCacheAt  time.Time
	updateCacheTTL = 10 * time.Minute
)

// 版本号归一化：去掉 v 前缀/空格，只留数字与点，便于比较
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if i := strings.IndexAny(v, "+"); i >= 0 {
		v = v[:i]
	}
	return v
}

// compareVersion 返回 -1/0/1；纯数字分段比较，段数不同按缺失补 0
func compareVersion(a, b string) int {
	as := strings.Split(normalizeVersion(a), ".")
	bs := strings.Split(normalizeVersion(b), ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(as) {
			ai = leadingNumber(as[i])
		}
		if i < len(bs) {
			bi = leadingNumber(bs[i])
		}
		if ai != bi {
			if ai > bi {
				return 1
			}
			return -1
		}
	}
	return 0
}

func leadingNumber(s string) int {
	digits := ""
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits += string(r)
			continue
		}
		break
	}
	if digits == "" {
		return 0
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return n
}

func fetchLatestRelease() (*githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ShieldDNS/"+appVersion)
	client := &http.Client{
		Timeout:   12 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 GitHub：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusNotFound:
			return nil, fmt.Errorf("GitHub 上还没有 Release（%s）", githubRepo)
		case http.StatusForbidden:
			return nil, fmt.Errorf("GitHub 拒绝了请求（可能是公司网络/代理的限制，HTTP 403）")
		default:
			return nil, fmt.Errorf("GitHub 返回 HTTP %d", resp.StatusCode)
		}
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析 Release 失败：%v", err)
	}
	return &rel, nil
}

// pickDownload 优先挑 Windows 压缩包，其次第一个资产
func pickDownload(rel *githubRelease) (string, int64) {
	assets := append([]releaseAsset{}, rel.Assets...)
	sort.SliceStable(assets, func(i, j int) bool {
		ai := assetScore(assets[i].Name)
		aj := assetScore(assets[j].Name)
		if ai != aj {
			return ai > aj
		}
		return assets[i].Size > assets[j].Size
	})
	for _, a := range assets {
		if assetScore(a.Name) > 0 {
			return a.URL, a.Size
		}
	}
	if len(assets) > 0 {
		return assets[0].URL, assets[0].Size
	}
	return releasePageURL, 0
}

// assetScore：win-x64 包 3 分，其它 win 包 2 分，杂项 0 分
func assetScore(name string) int {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".zip") {
		return 0
	}
	if strings.Contains(lower, "win") && strings.Contains(lower, "x64") {
		return 3
	}
	if strings.Contains(lower, "win") {
		return 2
	}
	return 1
}

// checkUpdate 带缓存地查一次；force=true 时跳过缓存
func checkUpdate(force bool) updateResult {
	updateMu.Lock()
	defer updateMu.Unlock()

	if !force && updateCache != nil && time.Since(updateCacheAt) < updateCacheTTL {
		return *updateCache
	}

	now := time.Now().Format("2006-01-02 15:04:05")
	res := updateResult{Current: appVersion, Repo: githubRepo, CheckedAt: now}
	rel, err := fetchLatestRelease()
	if err != nil {
		res.Error = err.Error()
		res.ReleaseURL = releasePageURL
		updateCache = &res
		updateCacheAt = time.Now()
		return res
	}
	res.OK = true
	res.Tag = rel.Tag
	res.Name = rel.Name
	res.Latest = normalizeVersion(rel.Tag)
	res.PublishedAt = rel.PublishedAt
	res.ReleaseURL = rel.PageURL
	if res.ReleaseURL == "" {
		res.ReleaseURL = releasePageURL
	}
	res.DownloadURL, res.Size = pickDownload(rel)
	res.HasUpdate = compareVersion(res.Latest, appVersion) > 0
	res.Notes = firstLines(rel.Body, 6)
	updateCache = &res
	updateCacheAt = time.Now()
	return res
}

// firstLines 取 Release 说明的前几行做摘要，避免把整篇 changelog 塞进界面
func firstLines(s string, n int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(strings.TrimSpace(s), "\n")
	out := make([]string, 0, n)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) >= n {
			break
		}
	}
	return strings.Join(out, "\n")
}

func (a *App) handleUpdate(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1"
	res := checkUpdate(force)
	writeJSON(w, http.StatusOK, res)
}

// handleOpenRelease 用系统默认浏览器打开 Release 页：
// WebView2 里直接跳转会把这个窗口变成浏览器，不如交给外面。
// 只放行本仓库的 GitHub 链接，避免变成任意 URL 的打开器。
func (a *App) handleOpenRelease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	target := strings.TrimSpace(req.URL)
	if target == "" {
		target = releasePageURL
	}
	allowed := "https://github.com/" + githubRepo
	if !strings.HasPrefix(target, allowed) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "只允许打开本项目的 GitHub 页面"})
		return
	}
	openInBrowser(target)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
