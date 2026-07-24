package app

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	errInvalidPath = errors.New("无效路径")
	errSymlinkPath = errors.New("不允许访问符号链接")
)

type pathResolver struct {
	root       string
	hostPrefix string
}

func newPathResolver(root, hostPrefix string) pathResolver {
	return pathResolver{
		root:       filepath.Clean(root),
		hostPrefix: filepath.Clean(hostPrefix),
	}
}

func (p pathResolver) resolveExisting(raw string) (string, string, error) {
	relative, absolute, err := p.resolve(raw)
	if err != nil {
		return "", "", err
	}
	if err := p.rejectSymlinkComponents(absolute); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", errSymlinkPath
	}
	evaluated, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", err
	}
	if !p.withinRoot(evaluated) {
		return "", "", errInvalidPath
	}
	return relative, absolute, nil
}

func (p pathResolver) resolveParent(raw string) (string, string, error) {
	relative, absolute, err := p.resolve(raw)
	if err != nil {
		return "", "", err
	}
	parent := filepath.Dir(absolute)
	if err := p.rejectSymlinkComponents(parent); err != nil {
		return "", "", err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return "", "", err
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", errSymlinkPath
	}
	evaluated, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", "", err
	}
	if !p.withinRoot(evaluated) {
		return "", "", errInvalidPath
	}
	return relative, absolute, nil
}

func (p pathResolver) resolve(raw string) (string, string, error) {
	if strings.ContainsRune(raw, 0) || strings.Contains(raw, "\\") {
		return "", "", errInvalidPath
	}
	cleaned := path.Clean("/" + strings.TrimSpace(raw))
	relative := strings.TrimPrefix(cleaned, "/")
	if relative == "." {
		relative = ""
	}
	if relative == metadataDirectoryName || strings.HasPrefix(relative, metadataDirectoryName+"/") {
		return "", "", errInvalidPath
	}
	absolute := filepath.Clean(filepath.Join(p.root, filepath.FromSlash(relative)))
	if !p.withinRoot(absolute) {
		return "", "", errInvalidPath
	}
	return filepath.ToSlash(relative), absolute, nil
}

func (p pathResolver) withinRoot(candidate string) bool {
	relative, err := filepath.Rel(p.root, filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func (p pathResolver) rejectSymlinkComponents(candidate string) error {
	relative, err := filepath.Rel(p.root, filepath.Clean(candidate))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errInvalidPath
	}
	if relative == "." {
		return nil
	}
	current := p.root
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errSymlinkPath
		}
	}
	return nil
}

func (p pathResolver) serverPath(relative string) string {
	if relative == "" {
		return p.hostPrefix
	}
	return filepath.Join(p.hostPrefix, filepath.FromSlash(relative))
}

func cleanFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", errors.New("文件名不能为空")
	}
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", errInvalidPath
	}
	for _, character := range name {
		if character == 0 || unicode.IsControl(character) {
			return "", fmt.Errorf("文件名包含不支持的字符")
		}
	}
	if len([]byte(name)) > 255 {
		return "", errors.New("文件名过长")
	}
	return name, nil
}
