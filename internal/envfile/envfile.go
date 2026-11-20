package envfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gofrs/flock"
)

const tempPattern = ".env.local.*.tmp"

var literalValue = regexp.MustCompile(`^(?:"([^"]*)"|'([^']*)'|([^\t "'\x60]*))(?:[\t ]+#.*)?$`)

// Value returns the trimmed value of the last assignment for key.
func Value(path, key string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return ParseValue(string(content), key)
}

// ParseValue reads a literal assignment from a snapshot of the env file.
func ParseValue(content, key string) (string, error) {
	lines := strings.SplitAfter(content, "\n")
	index, _, value := findAssignment(lines, key)
	if index < 0 {
		return "", nil
	}
	match := literalValue.FindStringSubmatch(strings.TrimRight(value, " \t"))
	if match == nil {
		return "", fmt.Errorf("%s 配置格式无效", key)
	}
	return match[1] + match[2] + match[3], nil
}

// CheckWritable checks the file and its directory before starting work that needs to update it.
func CheckWritable(path string) error {
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("锁定环境文件：%w", err)
	}
	defer lock.Close()
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	if err := errors.Join(statErr, file.Close()); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("环境文件不是普通文件")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), tempPattern)
	if err != nil {
		return err
	}
	return errors.Join(temp.Close(), os.Remove(temp.Name()))
}

// Update rewrites the last assignment for key under a lock shared by all config writers.
// The lock lives in a separate file so it remains valid after replacing the config file.
func Update(path, key, value string) error {
	return update(path, key, value, nil)
}

// UpdateIfUnchanged updates key only if the file still matches snapshot.
func UpdateIfUnchanged(path, key, value, snapshot string) error {
	return update(path, key, value, &snapshot)
}

func update(path, key, value string, snapshot *string) error {
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("锁定环境文件：%w", err)
	}
	defer lock.Close()

	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if snapshot != nil && string(content) != *snapshot {
		return errors.New("环境文件在提交前已更改")
	}
	updated := updateAssignment(string(content), key, value)
	if updated == string(content) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), tempPattern)
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := temp.WriteString(updated); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func findAssignment(lines []string, key string) (index int, body string, previous string) {
	for index := len(lines) - 1; index >= 0; index-- {
		line := lines[index]
		body := strings.TrimRight(line, "\r\n")
		setting := strings.TrimLeft(body, " \t")
		if rest, ok := strings.CutPrefix(setting, "export"); ok && len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
			setting = strings.TrimLeft(rest, " \t")
		}
		previous, ok := strings.CutPrefix(setting, key+"=")
		if ok {
			return index, body, previous
		}
	}
	return -1, "", ""
}

func updateAssignment(content, key, value string) string {
	lines := strings.SplitAfter(content, "\n")
	index, body, previous := findAssignment(lines, key)
	if index >= 0 {
		lines[index] = body[:len(body)-len(previous)] + value + lines[index][len(body):]
		return strings.Join(lines, "")
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + key + "=" + value + "\n"
}
