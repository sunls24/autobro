package slregister

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"autobro/internal/logging"
	"autobro/internal/mail"
	"github.com/sunls24/gox"
)

const (
	defaultSimpleLoginBase     = "https://app.simplelogin.io/api"
	registrationPasswordLength = 24
	maxResponseBodySize        = 1 << 20
	envTempPattern             = ".env.local.*.tmp"
)

const registrationSunMailDomain = "chato.eu.org"

// Options describes one SimpleLogin account registration.
type Options struct {
	EnvPath            string
	SunMailAPIKey      string
	SimpleLoginBaseURL string
	SunMailBaseURL     string
	HTTPClient         *http.Client
	Device             string
}

// Result contains the email address used to register the account. The
// generated password is intentionally not returned or persisted.
type Result struct {
	Email string
}

type apiClient struct {
	baseURL string
	client  *http.Client
}

type loginResponse struct {
	APIKey     string `json:"api_key"`
	MFAEnabled bool   `json:"mfa_enabled"`
}

// Register creates and activates one SimpleLogin account, obtains its login
// API key, and prepends that key to envPath. It does not delete aliases or
// accounts.
func Register(ctx context.Context, options Options) (Result, error) {
	if strings.TrimSpace(options.SunMailAPIKey) == "" {
		return Result{}, errors.New("SimpleLogin 注册失败：SUNMAIL_API_KEY 为空")
	}
	if strings.TrimSpace(options.EnvPath) == "" {
		return Result{}, errors.New("SimpleLogin 注册失败：.env.local 路径为空")
	}
	if err := checkEnvWritable(options.EnvPath); err != nil {
		return Result{}, fmt.Errorf("检查 %s 写入能力失败：%w", options.EnvPath, err)
	}

	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	simpleLoginBase := normalizeBaseURL(options.SimpleLoginBaseURL, defaultSimpleLoginBase)

	sunMailConfig := mail.SunMailConfig{
		APIKey:     options.SunMailAPIKey,
		BaseURL:    options.SunMailBaseURL,
		HTTPClient: httpClient,
		Domains:    []string{registrationSunMailDomain},
	}

	name := mail.GenerateName()
	sunMail := mail.NewSunMailWithConfig(sunMailConfig)
	address, err := sunMail.NewAddress(ctx, name)
	if err != nil {
		return Result{}, fmt.Errorf("生成 SunMail 地址失败：%w", err)
	}
	password, err := generatePassword()
	if err != nil {
		return Result{}, fmt.Errorf("生成 SimpleLogin 密码失败：%w", err)
	}

	logging.Step("SimpleLogin", "注册账号", loggingFields(name, address, registrationSunMailDomain)...)
	mailCtx, cancelMail := context.WithCancel(ctx)
	defer cancelMail()
	codeCh := sunMail.WaitMailCode(mailCtx, address)

	sl := apiClient{baseURL: simpleLoginBase, client: httpClient}
	if err := sl.postJSON(ctx, "/auth/register", map[string]string{
		"email":    address,
		"password": password,
	}, "", nil); err != nil {
		return Result{}, fmt.Errorf("SimpleLogin 注册请求失败：%w", err)
	}
	logging.Done("SimpleLogin", "注册请求")

	code, err := receiveCode(ctx, codeCh)
	if err != nil {
		return Result{}, fmt.Errorf("获取 SimpleLogin 验证码失败：%w", err)
	}
	logging.Done("SimpleLogin", "收取验证邮件")

	if err := sl.postJSON(ctx, "/auth/activate", map[string]string{
		"email": address,
		"code":  code,
	}, "", nil); err != nil {
		return Result{}, fmt.Errorf("SimpleLogin 激活账号失败：%w", err)
	}
	logging.Done("SimpleLogin", "账号激活")

	device := strings.TrimSpace(options.Device)
	if device == "" {
		device = "autobro-" + time.Now().UTC().Format("20060102-150405")
	}
	var login loginResponse
	if err := sl.postJSON(ctx, "/auth/login", map[string]string{
		"email":    address,
		"password": password,
		"device":   device,
	}, "", &login); err != nil {
		return Result{}, fmt.Errorf("SimpleLogin 登录失败：%w", err)
	}
	apiKey := strings.TrimSpace(login.APIKey)
	if apiKey == "" {
		if login.MFAEnabled {
			return Result{}, errors.New("SimpleLogin 登录未返回 API Key：账号启用了 MFA")
		}
		return Result{}, errors.New("SimpleLogin 登录未返回 API Key")
	}

	if err := PrependAPIKey(options.EnvPath, apiKey); err != nil {
		return Result{}, fmt.Errorf("更新 %s 失败：%w", options.EnvPath, err)
	}
	logging.Done("SimpleLogin", "写入 API Key", loggingFields(name, address, registrationSunMailDomain)...)

	return Result{Email: address}, nil
}

func loggingFields(name, address, domain string) []any {
	return []any{
		// 邮箱是本轮流程的定位信息；密码和 API Key 永远不进入日志。
		slog.String("name", name),
		slog.String("email", address),
		slog.String("domain", domain),
	}
}

func receiveCode(ctx context.Context, codeCh <-chan gox.Result[string]) (string, error) {
	select {
	case result, ok := <-codeCh:
		if !ok {
			return "", errors.New("验证码通道已关闭")
		}
		if result.Err != nil {
			return "", result.Err
		}
		code := strings.TrimSpace(result.Value)
		if code == "" {
			return "", errors.New("验证码为空")
		}
		return code, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c apiClient) postJSON(ctx context.Context, path string, payload any, authentication string, output any) error {
	return c.doJSON(ctx, http.MethodPost, path, payload, authentication, output)
}

func (c apiClient) doJSON(ctx context.Context, method, path string, payload any, authentication string, output any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if authentication != "" {
		req.Header.Set("Authentication", authentication)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := readResponseBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return responseError(resp.StatusCode, responseBody)
	}
	if output == nil || len(responseBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("解析响应：%w", err)
	}
	return nil
}

func readResponseBody(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func responseError(status int, body []byte) error {
	var response struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	message := ""
	if json.Unmarshal(body, &response) == nil {
		message = strings.TrimSpace(response.Error)
		if message == "" {
			message = strings.TrimSpace(response.Message)
		}
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("HTTP %d: %s", status, message)
}

func generatePassword() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	password := make([]byte, registrationPasswordLength)
	randomBytes := make([]byte, len(password)-1)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	for i, value := range randomBytes {
		password[i] = alphabet[int(value)%len(alphabet)]
	}
	password[len(password)-1] = '!'
	return string(password), nil
}

func normalizeBaseURL(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return strings.TrimRight(value, "/")
}

func checkEnvWritable(envPath string) error {
	file, err := os.Open(envPath)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return statErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !info.Mode().IsRegular() {
		return errors.New("环境文件不是普通文件")
	}

	temp, err := os.CreateTemp(filepath.Dir(envPath), envTempPattern)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return os.Remove(tempPath)
}

// PrependAPIKey prepends apiKey to SL_API_KEY in envPath. Existing keys are
// preserved and a key already present in the file is not duplicated.
func PrependAPIKey(envPath, apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || strings.ContainsAny(apiKey, ",\r\n") {
		return errors.New("invalid SimpleLogin API Key")
	}
	content, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	updated := updateEnvContent(string(content), apiKey)
	if updated == string(content) {
		return nil
	}
	info, err := os.Stat(envPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(envPath)
	temp, err := os.CreateTemp(dir, envTempPattern)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.WriteString(updated); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, envPath); err != nil {
		return err
	}
	return nil
}

func updateEnvContent(content, apiKey string) string {
	lines := strings.SplitAfter(content, "\n")
	matched := -1
	var matchedValues []string
	var matchedPrefix string
	var matchedEnding string
	for index, line := range lines {
		body, ending := splitLineEnding(line)
		leadingLength := len(body) - len(strings.TrimLeft(body, " \t"))
		leading := body[:leadingLength]
		trimmed := body[leadingLength:]
		exportPrefix := ""
		if strings.HasPrefix(trimmed, "export ") {
			exportPrefix = "export "
			trimmed = strings.TrimPrefix(trimmed, exportPrefix)
		}
		if !strings.HasPrefix(trimmed, "SL_API_KEY=") {
			continue
		}
		existing := parseCSVEnvValue(strings.TrimPrefix(trimmed, "SL_API_KEY="))
		matched = index
		matchedValues = existing
		matchedPrefix = leading + exportPrefix + "SL_API_KEY="
		matchedEnding = ending
	}
	if matched >= 0 {
		values := prependUnique(matchedValues, apiKey)
		lines[matched] = matchedPrefix + strings.Join(values, ",") + matchedEnding
		return strings.Join(lines, "")
	}

	updated := content
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += "SL_API_KEY=" + apiKey + "\n"
	return updated
}

func splitLineEnding(line string) (string, string) {
	if strings.HasSuffix(line, "\r\n") {
		return strings.TrimSuffix(line, "\r\n"), "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return strings.TrimSuffix(line, "\n"), "\n"
	}
	return line, ""
}

func parseCSVEnvValue(value string) []string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func prependUnique(existing []string, value string) []string {
	for _, item := range existing {
		if item == value {
			return existing
		}
	}
	return append([]string{value}, existing...)
}
