package web

import (
	"archive/zip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/chromedp/chromedp"
)

const (
	dalipanAPIBase                = "https://fc-resource-node-api.krzb.net"
	dalipanOrigin                 = "https://www.dalipan.com"
	downloadCredentialKeyMaterial = "ainovel-private-download-bundle-v1-20260716"
	baiduPCSVersion               = "v4.0.1"
	baiduPCSArchiveSHA256         = "bb3bc2409e55820d681daef880a6949748fbc81d740952c2b2c05c28a075a4ee"
	baiduPCSDownloadTimeout       = 10 * time.Minute
	baiduSmartSearchTimeout       = 45 * time.Second
	simulationSearchCandidateTTL  = 30 * time.Minute
)

var errDalipanQueryRejected = errors.New("大力盘拒绝了该搜索词")

var sensitiveURLPattern = regexp.MustCompile(`https?://\S+`)

//go:embed simulation_download_credentials.enc
var encryptedSimulationDownloadCredentials string

type simulationDownloadCredentials struct {
	Version        int             `json:"version"`
	XAuthorization string          `json:"x_authorization"`
	BaiduPCSConfig json.RawMessage `json:"baidu_pcs_config"`
}

type dalipanSearchResponse struct {
	Resources []struct {
		Resource struct {
			ID       string          `json:"id"`
			Filename string          `json:"filename"`
			Size     json.RawMessage `json:"size"`
			Type     string          `json:"type"`
		} `json:"res"`
	} `json:"resources"`
}

type dalipanDetailResponse struct {
	URL      string          `json:"url"`
	Filename string          `json:"filename"`
	Size     json.RawMessage `json:"size"`
	Password string          `json:"pwd"`
	Valid    int             `json:"valid"`
}

type dalipanCandidate struct {
	RemoteID  string
	Type      string
	Name      string
	ShareURL  string
	Password  string
	ExpiresAt time.Time
}

type baiduSmartSearchResult struct {
	ShareURL string
	Password string
	Context  string
}

type dalipanBaiduDownloader struct {
	runtimeRoot string
	credentials simulationDownloadCredentials
	client      *http.Client
	mu          sync.Mutex
	candidates  map[string]dalipanCandidate
	installMu   sync.Mutex
	requestMu   sync.Mutex
	lastRequest time.Time
}

type credentialUnavailableDownloader struct {
	err error
}

func (d credentialUnavailableDownloader) Search(context.Context, string, int) ([]simulationSearchResult, error) {
	return nil, d.err
}

func (d credentialUnavailableDownloader) Download(context.Context, string, string) (downloadedSimulationSource, error) {
	return downloadedSimulationSource{}, d.err
}

func newSimulationSourceDownloader(runtimeRoot string) simulationSourceDownloader {
	credentials, err := loadSimulationDownloadCredentials()
	if err != nil {
		return credentialUnavailableDownloader{err: fmt.Errorf("%w: %v", errSimulationSearchUnavailable, err)}
	}
	return &dalipanBaiduDownloader{
		runtimeRoot: runtimeRoot,
		credentials: credentials,
		client:      &http.Client{Timeout: 60 * time.Second},
		candidates:  make(map[string]dalipanCandidate),
	}
}

func loadSimulationDownloadCredentials() (simulationDownloadCredentials, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encryptedSimulationDownloadCredentials))
	if err != nil {
		return simulationDownloadCredentials{}, errors.New("凭据包格式无效")
	}
	key := sha256.Sum256([]byte(downloadCredentialKeyMaterial))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return simulationDownloadCredentials{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return simulationDownloadCredentials{}, err
	}
	if len(raw) <= gcm.NonceSize() {
		return simulationDownloadCredentials{}, errors.New("凭据包不完整")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return simulationDownloadCredentials{}, errors.New("凭据包无法解密")
	}
	var credentials simulationDownloadCredentials
	if err := json.Unmarshal(plain, &credentials); err != nil {
		return simulationDownloadCredentials{}, errors.New("凭据包内容无效")
	}
	if credentials.Version != 1 || strings.TrimSpace(credentials.XAuthorization) == "" || len(credentials.BaiduPCSConfig) == 0 {
		return simulationDownloadCredentials{}, errors.New("凭据包缺少大力盘或百度网盘认证")
	}
	return credentials, nil
}

func (d *dalipanBaiduDownloader) Search(ctx context.Context, query string, limit int) ([]simulationSearchResult, error) {
	seen := make(map[string]struct{})
	results := make([]simulationSearchResult, 0, limit)
	appendResponse := func(response dalipanSearchResponse) error {
		for _, item := range response.Resources {
			name := strings.TrimSpace(item.Resource.Filename)
			if !strings.EqualFold(filepath.Ext(name), ".txt") {
				continue
			}
			if !simulationTitleMatches(query, name) {
				continue
			}
			key := item.Resource.ID + "\x00" + name
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			id, err := randomCandidateID()
			if err != nil {
				return err
			}
			d.storeCandidate(id, dalipanCandidate{
				RemoteID:  item.Resource.ID,
				Type:      item.Resource.Type,
				Name:      name,
				ExpiresAt: time.Now().Add(simulationSearchCandidateTTL),
			})
			results = append(results, simulationSearchResult{
				ID:       id,
				Name:     name,
				FileType: "txt",
				Size:     formatRemoteSize(item.Resource.Size),
			})
			if len(results) == limit {
				return nil
			}
		}
		return nil
	}
	for _, variant := range []string{query} {
		precise, err := d.search(ctx, variant, "", 1)
		if err != nil && !errors.Is(err, errDalipanQueryRejected) {
			return nil, err
		}
		if err == nil {
			if err := appendResponse(precise); err != nil {
				return nil, err
			}
			if len(results) == limit {
				return results, nil
			}
		}
		if len(results) > 0 {
			continue
		}
		for page := 1; page <= 5; page++ {
			matched, err := d.search(ctx, variant, "match", page)
			if err != nil && !errors.Is(err, errDalipanQueryRejected) {
				return nil, err
			}
			if err == nil {
				if err := appendResponse(matched); err != nil {
					return nil, err
				}
				if len(results) == limit {
					return results, nil
				}
				if len(matched.Resources) == 0 {
					break
				}
			}
			if errors.Is(err, errDalipanQueryRejected) {
				break
			}
		}
	}
	if len(results) == 0 {
		fallback, err := d.searchBaiduSmartApp(ctx, query)
		if err == nil && fallback.ShareURL != "" {
			id, idErr := randomCandidateID()
			if idErr != nil {
				return nil, idErr
			}
			name := query + ".txt"
			d.storeCandidate(id, dalipanCandidate{
				Name:      name,
				ShareURL:  fallback.ShareURL,
				Password:  fallback.Password,
				ExpiresAt: time.Now().Add(simulationSearchCandidateTTL),
			})
			results = append(results, simulationSearchResult{ID: id, Name: name, FileType: "txt", Size: "百度智能体兜底"})
		}
	}
	return results, nil
}

func (d *dalipanBaiduDownloader) search(ctx context.Context, query, searchType string, page int) (dalipanSearchResponse, error) {
	endpoint, _ := url.Parse(dalipanAPIBase + "/api/v1/pan/search")
	values := endpoint.Query()
	values.Set("kw", query)
	values.Set("page", strconv.Itoa(page))
	values.Set("line", "0")
	values.Set("site", "dalipan")
	values.Set("resType", "baidu")
	if searchType != "" {
		values.Set("searchType", searchType)
	}
	endpoint.RawQuery = values.Encode()

	var response dalipanSearchResponse
	if err := d.getJSON(ctx, endpoint.String(), &response); err != nil {
		return dalipanSearchResponse{}, err
	}
	return response, nil
}

func (d *dalipanBaiduDownloader) Download(ctx context.Context, resultID, destination string) (downloadedSimulationSource, error) {
	candidate, ok := d.takeCandidate(resultID)
	if !ok {
		return downloadedSimulationSource{}, errors.New("搜索结果已过期，请重新搜索")
	}
	if candidate.ShareURL != "" {
		return d.downloadWithBaiduPCS(ctx, destination, candidate.Name, candidate.ShareURL, candidate.Password)
	}
	detailEndpoint, _ := url.Parse(dalipanAPIBase + "/api/v1/pan/detail")
	values := detailEndpoint.Query()
	values.Set("id", candidate.RemoteID)
	values.Set("size", "15")
	values.Set("type", candidate.Type)
	detailEndpoint.RawQuery = values.Encode()
	var detail dalipanDetailResponse
	if err := d.getJSON(ctx, detailEndpoint.String(), &detail); err != nil {
		return downloadedSimulationSource{}, err
	}
	if detail.Valid != 1 || strings.TrimSpace(detail.URL) == "" {
		return downloadedSimulationSource{}, errors.New("所选资源的百度网盘链接已失效")
	}
	shareURL, err := url.Parse(detail.URL)
	if err != nil || !strings.EqualFold(shareURL.Hostname(), "pan.baidu.com") {
		return downloadedSimulationSource{}, errors.New("资源没有提供有效的百度网盘链接")
	}
	name := strings.TrimSpace(detail.Filename)
	if name == "" {
		name = candidate.Name
	}
	if !strings.EqualFold(filepath.Ext(name), ".txt") {
		return downloadedSimulationSource{}, errors.New("请选择 TXT 文件进行下载")
	}
	return d.downloadWithBaiduPCS(ctx, destination, name, shareURL.String(), detail.Password)
}

func (d *dalipanBaiduDownloader) searchBaiduSmartApp(ctx context.Context, query string) (baiduSmartSearchResult, error) {
	browserPath, err := installedBackgroundBrowser()
	if err != nil {
		return baiduSmartSearchResult{}, err
	}
	profileDir, err := os.MkdirTemp(d.runtimeRoot, ".baidu-smart-search-*")
	if err != nil {
		return baiduSmartSearchResult{}, err
	}
	defer os.RemoveAll(profileDir)

	searchCtx, cancel := context.WithTimeout(ctx, baiduSmartSearchTimeout)
	defer cancel()
	options := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"),
		chromedp.WindowSize(1365, 900),
	)
	allocatorCtx, allocatorCancel := chromedp.NewExecAllocator(searchCtx, options...)
	defer allocatorCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocatorCtx)
	defer browserCancel()

	var resultURL string
	for _, searchQuery := range []string{query + " txt", query} {
		searchURL := "https://www.baidu.com/s?wd=" + url.QueryEscape(searchQuery)
		if err := chromedp.Run(browserCtx,
			chromedp.Navigate(searchURL),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.Evaluate(`(() => {
			for (const heading of document.querySelectorAll('h3')) {
				const title = (heading.innerText || heading.textContent || '').trim();
				if (!title.includes('智能分身')) continue;
				const anchor = heading.closest('a') || heading.querySelector('a');
				if (anchor && anchor.href) return anchor.href;
			}
			return '';
		})()`, &resultURL),
		); err != nil {
			return baiduSmartSearchResult{}, fmt.Errorf("百度智能体兜底搜索失败: %w", err)
		}
		if strings.TrimSpace(resultURL) != "" {
			break
		}
	}
	if strings.TrimSpace(resultURL) == "" {
		return baiduSmartSearchResult{}, errors.New("百度搜索没有智能分身 TXT 结果")
	}

	var payload string
	if err := chromedp.Run(browserCtx,
		chromedp.Navigate(resultURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(8*time.Second),
		chromedp.Evaluate(`(() => {
			const docs = [document, ...Array.from(document.querySelectorAll('iframe'))
				.map(frame => { try { return frame.contentDocument; } catch (_) { return null; } })
				.filter(Boolean)];
			for (const doc of docs) {
				for (const anchor of doc.querySelectorAll('a')) {
					let parsed;
					try { parsed = new URL(anchor.href); } catch (_) { continue; }
					if (parsed.hostname !== 'pan.baidu.com') continue;
					let node = anchor;
					let context = '';
					for (let depth = 0; node && depth < 5; depth++, node = node.parentElement) {
						const candidate = (node.innerText || node.textContent || '').trim();
						if (candidate.length > context.length && candidate.length < 4000) context = candidate;
					}
					return JSON.stringify({share_url: anchor.href, context});
				}
			}
			return '';
		})()`, &payload),
	); err != nil {
		return baiduSmartSearchResult{}, fmt.Errorf("读取百度智能体结果失败: %w", err)
	}
	if payload == "" {
		return baiduSmartSearchResult{}, errors.New("百度智能体没有提供网盘链接")
	}
	var result struct {
		ShareURL string `json:"share_url"`
		Context  string `json:"context"`
	}
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return baiduSmartSearchResult{}, errors.New("百度智能体结果格式无效")
	}
	shareURL, err := url.Parse(result.ShareURL)
	if err != nil || !strings.EqualFold(shareURL.Hostname(), "pan.baidu.com") {
		return baiduSmartSearchResult{}, errors.New("百度智能体没有提供有效网盘链接")
	}
	password := strings.TrimSpace(shareURL.Query().Get("pwd"))
	if password == "" {
		password = extractBaiduSharePassword(result.Context)
	}
	candidate := baiduSmartSearchResult{ShareURL: shareURL.String(), Password: password, Context: result.Context}
	if !smartResultMatchesTXT(query, result.Context) {
		return candidate, errors.New("百度智能体结果不是匹配的 TXT 文件")
	}
	return candidate, nil
}

func installedBackgroundBrowser() (string, error) {
	paths := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
	}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", errors.New("未找到可用于后台搜索的 Edge 或 Chrome")
}

func smartResultMatchesTXT(query, contextText string) bool {
	hasTXT := strings.Contains(strings.ToLower(contextText), "txt")
	contextText = normalizedSimulationTitle(contextText)
	query = normalizedSimulationTitle(query)
	return query != "" && strings.Contains(contextText, query) && hasTXT
}

func simulationTitleMatches(query, filename string) bool {
	query = normalizedSimulationTitle(strings.TrimSuffix(query, filepath.Ext(query)))
	filename = normalizedSimulationTitle(strings.TrimSuffix(filename, filepath.Ext(filename)))
	return query != "" && strings.Contains(filename, query)
}

func normalizedSimulationTitle(value string) string {
	value = strings.ToLower(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("，。！？：；、,.!?:;《》()（）[]【】", r) {
			return -1
		}
		return r
	}, value)
}

var baiduPasswordPattern = regexp.MustCompile(`(?i)(?:提取码|密码|pwd)\s*[:：]?\s*([a-z0-9]{4})`)

func extractBaiduSharePassword(value string) string {
	match := baiduPasswordPattern.FindStringSubmatch(value)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func (d *dalipanBaiduDownloader) getJSON(ctx context.Context, endpoint string, target any) error {
	for attempt := 0; attempt < 2; attempt++ {
		if err := d.waitForRequestSlot(ctx); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		request.Header.Set("X-Authorization", d.credentials.XAuthorization)
		request.Header.Set("Origin", dalipanOrigin)
		request.Header.Set("Referer", dalipanOrigin+"/")
		response, err := d.client.Do(request)
		if err != nil {
			return fmt.Errorf("大力盘请求失败: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("读取大力盘响应失败: %w", readErr)
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return errors.New("大力盘登录凭据已过期，请重新导入 X-Authorization")
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("大力盘请求失败: HTTP %d", response.StatusCode)
		}
		if err := json.Unmarshal(body, target); err == nil {
			return nil
		} else if attempt == 1 {
			snippet := strings.TrimSpace(string(body))
			if strings.Contains(strings.ToLower(snippet), "copyright") {
				return errDalipanQueryRejected
			}
			if len([]rune(snippet)) > 80 {
				snippet = string([]rune(snippet)[:80])
			}
			return fmt.Errorf("解析大力盘响应失败: %w（响应：%s）", err, snippet)
		}
	}
	return errors.New("大力盘请求失败")
}

func (d *dalipanBaiduDownloader) waitForRequestSlot(ctx context.Context) error {
	d.requestMu.Lock()
	defer d.requestMu.Unlock()
	if wait := time.Until(d.lastRequest.Add(time.Second)); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	d.lastRequest = time.Now()
	return nil
}

func (d *dalipanBaiduDownloader) storeCandidate(id string, candidate dalipanCandidate) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for key, current := range d.candidates {
		if current.ExpiresAt.Before(now) {
			delete(d.candidates, key)
		}
	}
	d.candidates[id] = candidate
}

func (d *dalipanBaiduDownloader) takeCandidate(id string) (dalipanCandidate, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	candidate, ok := d.candidates[id]
	delete(d.candidates, id)
	return candidate, ok && candidate.ExpiresAt.After(time.Now())
}

func randomCandidateID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func formatRemoteSize(raw json.RawMessage) string {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	bytes, err := strconv.ParseInt(text, 10, 64)
	if err != nil || bytes <= 0 {
		return "大小未知"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	divisor, exponent := int64(unit), 0
	for value := bytes / unit; value >= unit && exponent < 3; value /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(divisor), "KMGT"[exponent])
}

func (d *dalipanBaiduDownloader) downloadWithBaiduPCS(ctx context.Context, destination, name, shareURL, password string) (downloadedSimulationSource, error) {
	executable, err := d.ensureBaiduPCS(ctx)
	if err != nil {
		return downloadedSimulationSource{}, err
	}
	configDir := filepath.Join(destination, "baidupcs-config")
	downloadDir := filepath.Join(destination, "download")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return downloadedSimulationSource{}, err
	}
	if err := os.MkdirAll(downloadDir, 0o700); err != nil {
		return downloadedSimulationSource{}, err
	}
	if err := os.WriteFile(filepath.Join(configDir, "pcs_config.json"), d.credentials.BaiduPCSConfig, 0o600); err != nil {
		return downloadedSimulationSource{}, fmt.Errorf("写入临时百度网盘凭据失败: %w", err)
	}
	if _, err := runBaiduPCS(ctx, executable, configDir, "config", "set", "-savedir", filepath.ToSlash(downloadDir)); err != nil {
		return downloadedSimulationSource{}, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, baiduPCSDownloadTimeout)
	defer cancel()
	output, err := runBaiduPCS(commandCtx, executable, configDir, "transfer", "--download", shareURL, password)
	if err != nil {
		return downloadedSimulationSource{}, err
	}
	if containsBaiduPCSFailure(output) {
		if strings.Contains(output, "文件重复") || strings.Contains(output, "已存在") {
			output, err = runBaiduPCS(commandCtx, executable, configDir, "download", "--ow", name)
			if err != nil {
				return downloadedSimulationSource{}, err
			}
			if containsBaiduPCSFailure(output) {
				return downloadedSimulationSource{}, fmt.Errorf("BaiduPCS-Go 下载远端同名文件失败: %s", baiduPCSFailureSummary(output))
			}
		} else {
			return downloadedSimulationSource{}, fmt.Errorf("BaiduPCS-Go 下载失败: %s", baiduPCSFailureSummary(output))
		}
	}
	path, size, err := newestNonEmptyFile(downloadDir)
	if err != nil {
		return downloadedSimulationSource{}, err
	}
	actualName := filepath.Base(path)
	if !strings.EqualFold(filepath.Ext(actualName), ".txt") {
		return downloadedSimulationSource{}, errors.New("请选择 TXT 文件进行下载")
	}
	if strings.TrimSpace(actualName) == "" {
		actualName = name
	}
	return downloadedSimulationSource{Name: actualName, Path: path, Size: size}, nil
}

func runBaiduPCS(ctx context.Context, executable, configDir string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = append(os.Environ(), "BAIDUPCS_GO_CONFIG_DIR="+configDir)
	output, err := command.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errors.New("BaiduPCS-Go 下载超时")
		}
		return "", fmt.Errorf("BaiduPCS-Go 执行失败: %w", err)
	}
	return string(output), nil
}

func containsBaiduPCSFailure(output string) bool {
	normalized := strings.ToLower(output)
	return strings.Contains(normalized, "失败") || strings.Contains(normalized, "错误") || strings.Contains(normalized, "未登录") || strings.Contains(normalized, "error")
}

func baiduPCSFailureSummary(output string) string {
	normalized := strings.ToLower(output)
	switch {
	case strings.Contains(output, "获取分享项元数据错误"):
		return "无法读取分享元数据，请检查百度网盘 STOKEN"
	case strings.Contains(output, "未登录") || strings.Contains(output, "登录失败"):
		return "百度网盘登录已过期"
	case strings.Contains(output, "已存在") || strings.Contains(normalized, "already exists"):
		return "百度网盘远端存在同名文件"
	case strings.Contains(output, "频繁") || strings.Contains(normalized, "too many"):
		return "百度网盘请求过于频繁"
	case strings.Contains(output, "权限") || strings.Contains(normalized, "permission"):
		return "百度网盘权限不足"
	default:
		return "BaiduPCS-Go 返回错误状态（" + sanitizedBaiduPCSTail(output) + "）"
	}
}

func sanitizedBaiduPCSTail(output string) string {
	redacted := sensitiveURLPattern.ReplaceAllString(output, "<url>")
	redacted = strings.Join(strings.Fields(redacted), " ")
	runes := []rune(redacted)
	if len(runes) > 300 {
		runes = runes[len(runes)-300:]
	}
	return string(runes)
}

func newestNonEmptyFile(root string) (string, int64, error) {
	type candidate struct {
		path string
		size int64
		time time.Time
	}
	var files []candidate
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 0 {
			files = append(files, candidate{path: path, size: info.Size(), time: info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	if len(files) == 0 {
		return "", 0, errors.New("BaiduPCS-Go 已结束，但没有找到有效的下载文件")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].time.After(files[j].time) })
	return files[0].path, files[0].size, nil
}

func (d *dalipanBaiduDownloader) ensureBaiduPCS(ctx context.Context) (string, error) {
	d.installMu.Lock()
	defer d.installMu.Unlock()
	toolRoot := filepath.Join(d.runtimeRoot, "tools", "BaiduPCS-Go-"+baiduPCSVersion)
	executable := filepath.Join(toolRoot, "BaiduPCS-Go-"+baiduPCSVersion+"-windows-x64", "BaiduPCS-Go.exe")
	if info, err := os.Stat(executable); err == nil && !info.IsDir() {
		return executable, nil
	}
	if err := os.MkdirAll(toolRoot, 0o755); err != nil {
		return "", err
	}
	archiveName := "BaiduPCS-Go-" + baiduPCSVersion + "-windows-x64.zip"
	downloadURL := "https://github.com/qjfoidnh/BaiduPCS-Go/releases/download/" + baiduPCSVersion + "/" + archiveName
	archivePath := filepath.Join(toolRoot, archiveName)
	var downloadErr error
	for attempt := 1; attempt <= 3; attempt++ {
		downloadErr = d.downloadBaiduPCSArchive(ctx, downloadURL, archivePath)
		if downloadErr == nil {
			break
		}
		if attempt < 3 {
			timer := time.NewTimer(time.Duration(attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	if downloadErr != nil {
		return "", fmt.Errorf("自动安装 BaiduPCS-Go 失败: %w", downloadErr)
	}
	if err := extractZipSafely(archivePath, toolRoot); err != nil {
		return "", err
	}
	if _, err := os.Stat(executable); err != nil {
		return "", errors.New("BaiduPCS-Go 自动安装完成后未找到可执行文件")
	}
	return executable, nil
}

func (d *dalipanBaiduDownloader) downloadBaiduPCSArchive(ctx context.Context, downloadURL, archivePath string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	response, err := d.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	partialPath := archivePath + ".part"
	archive, err := os.Create(partialPath)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(response.Body, 128<<20))
	closeErr := archive.Close()
	if copyErr != nil {
		_ = os.Remove(partialPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(partialPath)
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != baiduPCSArchiveSHA256 {
		_ = os.Remove(partialPath)
		return errors.New("安装包校验失败")
	}
	if err := os.Remove(archivePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(partialPath)
		return err
	}
	if err := os.Rename(partialPath, archivePath); err != nil {
		_ = os.Remove(partialPath)
		return err
	}
	return nil
}

func extractZipSafely(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	root, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		target := filepath.Join(root, filepath.FromSlash(file.Name))
		if !isSameOrChild(root, target) {
			return errors.New("BaiduPCS-Go 安装包包含不安全路径")
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		destinationFile, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(destinationFile, source)
		closeDestinationErr := destinationFile.Close()
		closeSourceErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeDestinationErr != nil {
			return closeDestinationErr
		}
		if closeSourceErr != nil {
			return closeSourceErr
		}
	}
	return nil
}
