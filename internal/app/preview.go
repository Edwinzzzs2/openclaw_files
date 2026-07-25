package app

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxPreviewXMLBytes    = int64(16 * 1024 * 1024)
	maxPreviewOfficeXML   = uint64(64 * 1024 * 1024)
	maxPreviewParagraphs  = 1200
	maxPreviewTextBytes   = 2 * 1024 * 1024
	maxPreviewSheets      = 3
	maxPreviewRows        = 200
	maxPreviewColumns     = 40
	maxPreviewSlides      = 100
	maxPreviewArchiveRows = 400
)

type previewSheet struct {
	Name      string     `json:"name"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated,omitempty"`
}

type previewSlide struct {
	Number     int      `json:"number"`
	Paragraphs []string `json:"paragraphs"`
}

type previewArchiveEntry struct {
	Name      string `json:"name"`
	Size      uint64 `json:"size"`
	Directory bool   `json:"directory,omitempty"`
}

func (s *Server) handleStructuredPreview(w http.ResponseWriter, r *http.Request) {
	_, absolute, err := s.paths.resolveExisting(r.URL.Query().Get("path"))
	if err != nil {
		s.writePathError(w, err)
		return
	}
	info, err := os.Stat(absolute)
	if err != nil {
		s.writePathError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, errors.New("目标不是普通文件"))
		return
	}
	contentType := mimeTypeForName(info.Name())
	kind := previewKind(info.Name(), contentType)
	if info.Size() > s.config.MaxPreviewSize {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Errorf("文件超过 %s，不支持在线预览", formatByteSize(s.config.MaxPreviewSize)))
		return
	}

	switch kind {
	case "document":
		paragraphs, truncated, err := previewDOCX(absolute)
		if err != nil {
			writePreviewError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":       kind,
			"paragraphs": paragraphs,
			"truncated":  truncated,
		})
	case "spreadsheet":
		sheets, err := previewXLSX(absolute)
		if err != nil {
			writePreviewError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":   kind,
			"sheets": sheets,
		})
	case "presentation":
		slides, truncated, err := previewPPTX(absolute)
		if err != nil {
			writePreviewError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":      kind,
			"slides":    slides,
			"truncated": truncated,
		})
	case "archive":
		entries, truncated, err := previewZIP(absolute)
		if err != nil {
			writePreviewError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":      kind,
			"entries":   entries,
			"truncated": truncated,
		})
	default:
		writeError(w, http.StatusBadRequest, errors.New("该文件不需要结构化预览"))
	}
}

func previewDOCX(filePath string) ([]string, bool, error) {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, false, errors.New("DOCX 文件格式无效")
	}
	defer reader.Close()
	document := findZipFile(reader.File, "word/document.xml")
	if document == nil {
		return nil, false, errors.New("DOCX 正文不存在")
	}
	paragraphs, truncated, err := extractParagraphs(document, maxPreviewParagraphs)
	if err != nil {
		return nil, false, errors.New("无法读取 DOCX 正文")
	}
	return paragraphs, truncated, nil
}

func previewPPTX(filePath string) ([]previewSlide, bool, error) {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, false, errors.New("PPTX 文件格式无效")
	}
	defer reader.Close()
	files := make([]*zip.File, 0)
	for _, file := range reader.File {
		if strings.HasPrefix(file.Name, "ppt/slides/slide") &&
			strings.HasSuffix(file.Name, ".xml") &&
			!strings.Contains(file.Name, "/_rels/") {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return numberInName(files[i].Name) < numberInName(files[j].Name)
	})
	truncated := len(files) > maxPreviewSlides
	if len(files) > maxPreviewSlides {
		files = files[:maxPreviewSlides]
	}
	if !withinPreviewXMLBudget(files) {
		return nil, false, errors.New("PPTX 幻灯片内容过大")
	}
	slides := make([]previewSlide, 0, len(files))
	for index, file := range files {
		paragraphs, paragraphTruncated, err := extractParagraphs(file, 200)
		if err != nil {
			return nil, false, errors.New("无法读取 PPTX 幻灯片")
		}
		truncated = truncated || paragraphTruncated
		slides = append(slides, previewSlide{
			Number:     index + 1,
			Paragraphs: paragraphs,
		})
	}
	return slides, truncated, nil
}

func previewXLSX(filePath string) ([]previewSheet, error) {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, errors.New("XLSX 文件格式无效")
	}
	defer reader.Close()

	sharedStrings := []string{}
	if sharedFile := findZipFile(reader.File, "xl/sharedStrings.xml"); sharedFile != nil {
		sharedStrings, err = extractSharedStrings(sharedFile)
		if err != nil {
			return nil, errors.New("无法读取 XLSX 共享文本")
		}
	}
	worksheets := make([]*zip.File, 0)
	for _, file := range reader.File {
		if strings.HasPrefix(file.Name, "xl/worksheets/sheet") &&
			strings.HasSuffix(file.Name, ".xml") {
			worksheets = append(worksheets, file)
		}
	}
	sort.Slice(worksheets, func(i, j int) bool {
		return numberInName(worksheets[i].Name) < numberInName(worksheets[j].Name)
	})
	if len(worksheets) > maxPreviewSheets {
		worksheets = worksheets[:maxPreviewSheets]
	}
	budgetFiles := append([]*zip.File(nil), worksheets...)
	if sharedFile := findZipFile(reader.File, "xl/sharedStrings.xml"); sharedFile != nil {
		budgetFiles = append(budgetFiles, sharedFile)
	}
	if !withinPreviewXMLBudget(budgetFiles) {
		return nil, errors.New("XLSX 工作簿内容过大")
	}
	sheets := make([]previewSheet, 0, len(worksheets))
	for index, worksheet := range worksheets {
		rows, truncated, err := extractWorksheet(worksheet, sharedStrings)
		if err != nil {
			return nil, errors.New("无法读取 XLSX 工作表")
		}
		sheets = append(sheets, previewSheet{
			Name:      fmt.Sprintf("工作表 %d", index+1),
			Rows:      rows,
			Truncated: truncated,
		})
	}
	return sheets, nil
}

func previewZIP(filePath string) ([]previewArchiveEntry, bool, error) {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, false, errors.New("ZIP 文件格式无效")
	}
	defer reader.Close()
	truncated := len(reader.File) > maxPreviewArchiveRows
	files := reader.File
	if len(files) > maxPreviewArchiveRows {
		files = files[:maxPreviewArchiveRows]
	}
	entries := make([]previewArchiveEntry, 0, len(files))
	for _, file := range files {
		entries = append(entries, previewArchiveEntry{
			Name:      file.Name,
			Size:      file.UncompressedSize64,
			Directory: file.FileInfo().IsDir(),
		})
	}
	return entries, truncated, nil
}

func extractParagraphs(file *zip.File, limit int) ([]string, bool, error) {
	if file.UncompressedSize64 > uint64(maxPreviewXMLBytes) {
		return nil, false, errors.New("文档内容过大")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, false, err
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, maxPreviewXMLBytes+1))
	paragraphs := make([]string, 0)
	var paragraph strings.Builder
	inParagraph := 0
	inText := 0
	totalBytes := 0
	truncated := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "p":
				inParagraph++
				if inParagraph == 1 {
					paragraph.Reset()
				}
			case "t":
				if inParagraph > 0 {
					inText++
				}
			case "tab":
				if inParagraph > 0 {
					paragraph.WriteByte('\t')
				}
			case "br":
				if inParagraph > 0 {
					paragraph.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inText > 0 && inParagraph > 0 {
				remaining := maxPreviewTextBytes - totalBytes - paragraph.Len()
				if remaining <= 0 {
					truncated = true
					continue
				}
				text := string(value)
				if len(text) > remaining {
					text = text[:remaining]
					truncated = true
				}
				paragraph.WriteString(text)
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				if inText > 0 {
					inText--
				}
			case "p":
				if inParagraph > 0 {
					inParagraph--
				}
				if inParagraph == 0 {
					text := strings.TrimSpace(paragraph.String())
					if text != "" {
						paragraphs = append(paragraphs, text)
						totalBytes += len(text)
						if len(paragraphs) >= limit || totalBytes >= maxPreviewTextBytes {
							return paragraphs, true, nil
						}
					}
				}
			}
		}
	}
	return paragraphs, truncated, nil
}

func extractSharedStrings(file *zip.File) ([]string, error) {
	if file.UncompressedSize64 > uint64(maxPreviewXMLBytes) {
		return nil, errors.New("共享文本过大")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, maxPreviewXMLBytes+1))
	values := make([]string, 0)
	var current strings.Builder
	inString := 0
	inText := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return values, nil
		}
		if err != nil {
			return nil, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local == "si" {
				inString++
				current.Reset()
			} else if value.Name.Local == "t" && inString > 0 {
				inText++
			}
		case xml.CharData:
			if inText > 0 {
				current.Write(value)
			}
		case xml.EndElement:
			if value.Name.Local == "t" && inText > 0 {
				inText--
			} else if value.Name.Local == "si" && inString > 0 {
				inString--
				values = append(values, current.String())
			}
		}
	}
}

func extractWorksheet(file *zip.File, sharedStrings []string) ([][]string, bool, error) {
	if file.UncompressedSize64 > uint64(maxPreviewXMLBytes) {
		return nil, false, errors.New("工作表内容过大")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, false, err
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, maxPreviewXMLBytes+1))
	rows := make([][]string, 0)
	var row []string
	cellReference := ""
	cellType := ""
	var cellValue strings.Builder
	inCell := false
	inValue := 0
	truncated := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return rows, truncated, nil
		}
		if err != nil {
			return nil, false, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "row":
				row = nil
			case "c":
				inCell = true
				cellReference = xmlAttribute(value.Attr, "r")
				cellType = xmlAttribute(value.Attr, "t")
				cellValue.Reset()
			case "v", "t":
				if inCell {
					inValue++
				}
			}
		case xml.CharData:
			if inCell && inValue > 0 {
				cellValue.Write(value)
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "v", "t":
				if inValue > 0 {
					inValue--
				}
			case "c":
				column := worksheetColumn(cellReference)
				if column >= maxPreviewColumns {
					truncated = true
				} else {
					for len(row) <= column {
						row = append(row, "")
					}
					value := cellValue.String()
					if cellType == "s" {
						index, parseErr := strconv.Atoi(value)
						if parseErr == nil && index >= 0 && index < len(sharedStrings) {
							value = sharedStrings[index]
						}
					}
					row[column] = value
				}
				inCell = false
			case "row":
				for len(row) > 0 && row[len(row)-1] == "" {
					row = row[:len(row)-1]
				}
				if len(row) > 0 {
					rows = append(rows, row)
				}
				if len(rows) >= maxPreviewRows {
					return rows, true, nil
				}
			}
		}
	}
}

func findZipFile(files []*zip.File, name string) *zip.File {
	for _, file := range files {
		if file.Name == name {
			return file
		}
	}
	return nil
}

func withinPreviewXMLBudget(files []*zip.File) bool {
	var total uint64
	for _, file := range files {
		if file.UncompressedSize64 > maxPreviewOfficeXML-total {
			return false
		}
		total += file.UncompressedSize64
	}
	return true
}

func xmlAttribute(attributes []xml.Attr, name string) string {
	for _, attribute := range attributes {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

func worksheetColumn(reference string) int {
	column := 0
	found := false
	for _, character := range reference {
		if !unicode.IsLetter(character) {
			break
		}
		found = true
		character = unicode.ToUpper(character)
		column = column*26 + int(character-'A'+1)
	}
	if !found {
		return 0
	}
	return column - 1
}

func numberInName(name string) int {
	base := filepath.Base(name)
	number := 0
	found := false
	for _, character := range base {
		if unicode.IsDigit(character) {
			found = true
			number = number*10 + int(character-'0')
		} else if found {
			break
		}
	}
	return number
}

func writePreviewError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusUnprocessableEntity, err)
}

func formatByteSize(size int64) string {
	const mebibyte = int64(1024 * 1024)
	if size%mebibyte == 0 {
		return fmt.Sprintf("%d MiB", size/mebibyte)
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/float64(mebibyte))
}
