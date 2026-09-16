package uumailregister

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const envTempPattern = ".env.local.*.tmp"

// SetEnvListValue 把 value 前插去重更新到 envPath 中 key 对应的 CSV 列表；
// key 不存在时在文件末尾追加一行。写入通过临时文件 + rename 原子完成。
func SetEnvListValue(envPath, key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, ",\r\n") {
		return errors.New("无效的环境变量列表值")
	}
	content, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	updated := updateEnvList(string(content), key, value)
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
	return os.Rename(tempPath, envPath)
}

func updateEnvList(content, key, value string) string {
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
		if !strings.HasPrefix(trimmed, key+"=") {
			continue
		}
		existing := parseCSVEnvValue(strings.TrimPrefix(trimmed, key+"="))
		matched = index
		matchedValues = existing
		matchedPrefix = leading + exportPrefix + key + "="
		matchedEnding = ending
	}
	if matched >= 0 {
		values := prependUnique(matchedValues, value)
		lines[matched] = matchedPrefix + strings.Join(values, ",") + matchedEnding
		return strings.Join(lines, "")
	}

	updated := content
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += key + "=" + value + "\n"
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
	return nil
}
