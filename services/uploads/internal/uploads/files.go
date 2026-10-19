package uploads

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"
)

const maxFile = 8 << 20

func validFile(content []byte, ext string, maxRows int, formatted ...bool) error {
	allowFormatted := len(formatted) > 0 && formatted[0]
	if len(content) == 0 {
		return api(400, "empty_file")
	}
	if len(content) > maxFile {
		return api(413, "file_too_large")
	}
	switch ext {
	case "csv":
		if !utf8.Valid(content) {
			return api(400, "csv_utf8_required")
		}
		reader := csv.NewReader(bytes.NewReader(content))
		rows := 0
		for {
			row, e := reader.Read()
			if e == io.EOF {
				break
			}
			if e != nil || len(row) > 64 {
				return api(400, "invalid_csv")
			}
			rows++
			if rows > maxRows+1 {
				return api(413, "row_limit_exceeded")
			}
		}
	case "xlsx":
		z, e := zip.NewReader(bytes.NewReader(content), int64(len(content)))
		if e != nil {
			return api(400, "invalid_xlsx")
		}
		if len(z.File) > 2048 {
			return api(413, "zip_entry_limit")
		}
		seen := map[string]bool{}
		total := int64(0)
		book := false
		types := false
		for _, f := range z.File {
			name := strings.ToLower(f.Name)
			if path.Clean(f.Name) != f.Name || strings.HasPrefix(name, "/") || strings.Contains(name, "..") || seen[name] || f.Flags&1 != 0 {
				return api(400, "invalid_xlsx_archive")
			}
			seen[name] = true
			if strings.Contains(name, "vbaproject") || strings.Contains(name, "externallinks/") || strings.Contains(name, "embeddings/") {
				return api(400, "active_content_forbidden")
			}
			if f.UncompressedSize64 > 32<<20 || total+int64(f.UncompressedSize64) > 32<<20 {
				return api(413, "decompression_limit")
			}
			stream, e := f.Open()
			if e != nil {
				return api(400, "invalid_xlsx")
			}
			b, e := io.ReadAll(io.LimitReader(stream, (32<<20)-total+1))
			stream.Close()
			total += int64(len(b))
			if e != nil || total > 32<<20 {
				return api(413, "decompression_limit")
			}
			if name == "xl/workbook.xml" {
				book = true
			}
			if name == "[content_types].xml" {
				types = true
			}
			if strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".rels") {
				if bytes.Contains(bytes.ToUpper(bytes.ReplaceAll(b, []byte{0}, nil)), []byte("<!DOCTYPE")) || bytes.Contains(bytes.ToUpper(b), []byte("<!ENTITY")) {
					return api(400, "xml_entities_forbidden")
				}
				decoder := xml.NewDecoder(bytes.NewReader(b))
				rows := 0
				cells := 0
				for {
					token, e := decoder.Token()
					if e == io.EOF {
						break
					}
					if e != nil {
						return api(400, "invalid_xlsx_xml")
					}
					if el, ok := token.(xml.StartElement); ok {
						if el.Name.Local == "f" {
							return api(400, "formulas_forbidden")
						}
						if el.Name.Local == "row" {
							rows++
							cells = 0
							if rows > maxRows+11 {
								return api(413, "row_limit_exceeded")
							}
						}
						if el.Name.Local == "c" {
							cells++
							if cells > 64 && (!allowFormatted || cells > 16384) {
								return api(413, "column_limit_exceeded")
							}
						}
						for _, a := range el.Attr {
							if a.Name.Local == "TargetMode" && a.Value == "External" {
								return api(400, "external_relationship_forbidden")
							}
						}
					}
				}
			}
		}
		if !book || !types {
			return api(400, "invalid_xlsx")
		}
	case "json":
		if !json.Valid(content) {
			return api(400, "invalid_json")
		}
	case "js":
		// Only a bounded UTF-8 source is accepted. parseOwnerInput extracts
		// literal JSON without executing JavaScript; Tests validates its meaning.
		if !utf8.Valid(content) {
			return api(400, "source_utf8_required")
		}
	default:
		return api(415, "unsupported_file_format")
	}
	return nil
}
func readFile(r *http.Request) ([]byte, string, map[string]string, error) {
	if e := r.ParseMultipartForm(1 << 20); e != nil {
		return nil, "", nil, api(413, "invalid_or_oversized_upload")
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	f, h, e := r.FormFile("file")
	if e != nil {
		return nil, "", nil, api(400, "file_required")
	}
	defer f.Close()
	if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
		return nil, "", nil, api(400, "single_file_required")
	}
	b, e := io.ReadAll(io.LimitReader(f, maxFile+1))
	if e != nil || len(b) > maxFile {
		return nil, "", nil, api(413, "file_too_large")
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(h.Filename)), ".")
	fields := map[string]string{}
	allowed := map[string]bool{"type": true, "target_id": true, "direction_id": true, "theme_id": true, "create_new_theme": true, "new_theme_name": true, "title": true, "direction": true, "startDate": true, "endDate": true, "timeLimitMinutes": true, "published": true, "visible": true}
	for k, v := range r.MultipartForm.Value {
		if !allowed[k] || len(v) != 1 || len(v[0]) > 300 {
			return nil, "", nil, api(400, "invalid_upload_field")
		}
		fields[k] = v[0]
	}
	return b, ext, fields, nil
}
func ownerMultipart(b []byte, ext string, fields map[string]string) ([]byte, string, error) {
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	f, e := w.CreateFormFile("file", "source."+ext)
	if e != nil {
		return nil, "", e
	}
	f.Write(b)
	for k, v := range fields {
		if k != "type" && k != "target_id" {
			w.WriteField(k, v)
		}
	}
	w.Close()
	return buf.Bytes(), w.FormDataContentType(), nil
}
