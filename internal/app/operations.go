package app

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type pathRemap struct {
	From string
	To   string
}

func (s *Server) handleRenameSelection(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Directory string `json:"directory"`
		Path      string `json:"path"`
		Name      string `json:"name"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("重命名请求格式错误"))
		return
	}
	name, err := cleanFileName(request.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	targets, err := s.resolveSelectionTargets(request.Directory, []string{request.Path})
	if err != nil {
		s.writeSelectionError(w, err)
		return
	}
	target := targets[0]
	newRelative := path.Join(path.Dir("/"+target.relative), name)
	newRelative = strings.TrimPrefix(newRelative, "/")
	if newRelative == target.relative {
		writeError(w, http.StatusBadRequest, errors.New("新名称与当前名称相同"))
		return
	}
	_, newAbsolute, err := s.paths.resolveParent(newRelative)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	if _, err := os.Lstat(newAbsolute); err == nil {
		writeError(w, http.StatusConflict, errors.New("同名文件或文件夹已存在"))
		return
	} else if !os.IsNotExist(err) {
		s.writePathError(w, err)
		return
	}
	if err := os.Rename(target.absolute, newAbsolute); err != nil {
		if os.IsPermission(err) {
			writeError(w, http.StatusForbidden, errors.New("没有权限重命名所选项目"))
			return
		}
		writeError(w, http.StatusInternalServerError, errors.New("无法重命名所选项目"))
		return
	}
	if err := s.recent.remapPaths([]pathRemap{{From: target.relative, To: newRelative}}); err != nil {
		log.Printf("update recent paths after rename: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"name":       name,
		"path":       newRelative,
		"serverPath": s.paths.serverPath(newRelative),
	})
}

func (s *Server) handleMoveSelection(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Directory   string   `json:"directory"`
		Paths       []string `json:"paths"`
		Destination string   `json:"destination"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("移动请求格式错误"))
		return
	}
	targets, err := s.resolveSelectionTargets(request.Directory, request.Paths)
	if err != nil {
		s.writeSelectionError(w, err)
		return
	}
	destination, destinationAbsolute, err := s.paths.resolveExisting(request.Destination)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	destinationInfo, err := os.Stat(destinationAbsolute)
	if err != nil || !destinationInfo.IsDir() {
		writeError(w, http.StatusBadRequest, errors.New("移动目标目录不存在"))
		return
	}
	currentDirectory, _, err := s.paths.resolveExisting(request.Directory)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	if destination == currentDirectory {
		writeError(w, http.StatusBadRequest, errors.New("所选项目已在这个目录中"))
		return
	}

	type moveOperation struct {
		fromRelative string
		fromAbsolute string
		toRelative   string
		toAbsolute   string
	}
	operations := make([]moveOperation, 0, len(targets))
	for _, target := range targets {
		if target.info.IsDir() &&
			(destination == target.relative ||
				strings.HasPrefix(destination, target.relative+"/")) {
			writeError(w, http.StatusBadRequest, errors.New("不能把文件夹移动到自身或其子目录"))
			return
		}
		name := filepath.Base(target.absolute)
		toRelative := filepath.ToSlash(filepath.Join(destination, name))
		_, toAbsolute, err := s.paths.resolveParent(toRelative)
		if err != nil {
			s.writePathError(w, err)
			return
		}
		if _, err := os.Lstat(toAbsolute); err == nil {
			writeError(w, http.StatusConflict, errors.New("目标目录中存在同名文件或文件夹"))
			return
		} else if !os.IsNotExist(err) {
			s.writePathError(w, err)
			return
		}
		operations = append(operations, moveOperation{
			fromRelative: target.relative,
			fromAbsolute: target.absolute,
			toRelative:   toRelative,
			toAbsolute:   toAbsolute,
		})
	}

	moved := make([]moveOperation, 0, len(operations))
	for _, operation := range operations {
		if err := os.Rename(operation.fromAbsolute, operation.toAbsolute); err != nil {
			for index := len(moved) - 1; index >= 0; index-- {
				if rollbackErr := os.Rename(moved[index].toAbsolute, moved[index].fromAbsolute); rollbackErr != nil {
					log.Printf("rollback move %s: %v", moved[index].toRelative, rollbackErr)
				}
			}
			if os.IsPermission(err) {
				writeError(w, http.StatusForbidden, errors.New("没有权限移动所选项目"))
				return
			}
			writeError(w, http.StatusInternalServerError, errors.New("无法移动所选项目"))
			return
		}
		moved = append(moved, operation)
	}

	remaps := make([]pathRemap, 0, len(moved))
	response := make([]map[string]string, 0, len(moved))
	for _, operation := range moved {
		remaps = append(remaps, pathRemap{
			From: operation.fromRelative,
			To:   operation.toRelative,
		})
		response = append(response, map[string]string{
			"from": operation.fromRelative,
			"to":   operation.toRelative,
		})
	}
	if err := s.recent.remapPaths(remaps); err != nil {
		log.Printf("update recent paths after move: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": response})
}
