// Template import from a shared zip package. A reusable template is a
// directory with a template.json and an optional programs/ subtree; zipping
// that directory is what a user shares. This file turns such a zip back into an
// on-disk template under the templates dir, validating the payload (a valid
// template.json must be present) and guarding the extraction (no path
// traversal, a bounded archive so a malformed zip cannot exhaust disk).
package project

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxTemplateImportBytes bounds the total uncompressed size of an imported
// template package. Templates are small (a JSON file plus a handful of program
// resources); the cap stops a malformed or hostile zip from writing unbounded
// data during extraction.
const maxTemplateImportBytes = 64 << 20 // 64 MiB

// MaxTemplateImportBytes returns the cap on an imported template package's size.
// It is exposed so the HTTP layer can bound the request body before handing the
// bytes to ImportTemplateZip.
func MaxTemplateImportBytes() int64 { return maxTemplateImportBytes }

// ImportTemplateZip extracts a shared template package into the on-disk
// templates dir under name. The zip may carry the template at its root
// (template.json and optional programs/) or inside a single wrapping folder
// (e.g. <name>/template.json) which is stripped. The imported template must
// parse and validate; on any error nothing is written.
func ImportTemplateZip(name string, data []byte) (*TemplateConfig, error) {
	root := TemplatesDir()
	if name != "" {
		if _, err := os.Stat(TemplateDir(root, name)); err == nil {
			return nil, fmt.Errorf("project: template %q already exists", name)
		}
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("project: not a valid zip: %w", err)
	}

	base, tmplRaw, err := templateZipRoot(zr)
	if err != nil {
		return nil, err
	}
	tmpl, err := parseConfig(tmplRaw)
	if err != nil {
		return nil, err
	}

	// If no id was given, fall back to the wrapping directory's name (the zip
	// was probably exported as <name>/ or <name>.zip).
	if name == "" {
		if base == "" {
			return nil, fmt.Errorf("project: template id is required (zip has no wrapping folder)")
		}
		if name = sanitizeID(base); name == "" {
			return nil, fmt.Errorf("project: invalid template id derived from zip")
		}
	}

	destDir := TemplateDir(root, name)
	if err := extractTemplateZip(zr, base, destDir); err != nil {
		return nil, err
	}
	return tmpl, nil
}

// templateZipRoot inspects the archive and returns the folder prefix the
// template lives under ("", or a single wrapping directory) plus the contents
// of its template.json. A valid package has exactly one template.json; entries
// outside the resolved root are rejected so a stray file doesn't silently end
// up inside the template dir.
func templateZipRoot(zr *zip.Reader) (string, []byte, error) {
	// Locate the package's template.json by basename; a valid package has exactly
	// one, either at the zip root or under a single wrapping folder.
	var tmplPath string
	var tmplRaw []byte
	for _, f := range zr.File {
		clean := templateZipClean(f.Name)
		if clean == "" || clean == "." {
			continue
		}
		if path.Base(clean) != "template.json" {
			continue
		}
		if tmplPath != "" {
			return "", nil, fmt.Errorf("project: zip has more than one template.json")
		}
		tmplPath = clean
		r, err := f.Open()
		if err != nil {
			return "", nil, fmt.Errorf("project: read template.json: %w", err)
		}
		raw, err := io.ReadAll(io.LimitReader(r, maxTemplateImportBytes))
		r.Close()
		if err != nil {
			return "", nil, fmt.Errorf("project: read template.json: %w", err)
		}
		tmplRaw = raw
	}

	if tmplPath == "" {
		return "", nil, fmt.Errorf("project: zip has no template.json")
	}

	base := path.Dir(tmplPath)
	if base == "." {
		base = ""
	}
	// Only a single wrapping folder is supported — never a deeper nesting.
	if base != "" && strings.Contains(base, "/") {
		return "", nil, fmt.Errorf("project: template.json nested too deeply in zip")
	}

	// Every entry must live under the resolved root.
	for _, f := range zr.File {
		clean := templateZipClean(f.Name)
		if clean == "" || clean == "." || clean == base || clean == tmplPath {
			continue
		}
		if base == "" {
			// At the root every file is a bare name; a non-template sibling is
			// only valid if it is a directory subtree (e.g. programs/).
			if !strings.Contains(clean, "/") {
				return "", nil, fmt.Errorf("project: unexpected file %q at zip root", f.Name)
			}
			continue
		}
		if !strings.HasPrefix(clean, base+"/") {
			return "", nil, fmt.Errorf("project: zip mixes files inside and outside %q", base)
		}
	}
	return base, tmplRaw, nil
}

// templateZipClean normalizes a zip entry name: forward slashes and a single
// leading slash are tolerated, enclosing "." oddities collapse via path.Clean.
func templateZipClean(name string) string {
	return strings.TrimPrefix(path.Clean("/"+name), "/")
}

// extractTemplateZip writes every entry under base into destDir, rejecting any
// path that escapes it and bounding the total written bytes.
func extractTemplateZip(zr *zip.Reader, base, destDir string) error {
	var wrote int64
	for _, f := range zr.File {
		clean := strings.TrimPrefix(templateZipClean(f.Name), base+"/")
		if clean == "" || clean == "." || clean == base {
			continue
		}
		// zip-slip guard: the joined path must stay under destDir.
		dst := filepath.Join(destDir, filepath.FromSlash(clean))
		if !strings.HasPrefix(dst, filepath.Clean(destDir)+string(filepath.Separator)) {
			return fmt.Errorf("project: zip entry %q escapes the template dir", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return fmt.Errorf("project: read %s: %w", f.Name, err)
		}
		// Bound the copy so a single oversized entry cannot exhaust the budget
		// (the +1 flags "one byte too many" from a LimitReader as overflow).
		budget := maxTemplateImportBytes - wrote
		if f.UncompressedSize64 > uint64(budget) {
			os.RemoveAll(destDir)
			return fmt.Errorf("project: template package too large")
		}
		dstF, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			src.Close()
			return err
		}
		n, cpErr := io.CopyN(dstF, src, int64(budget)+1)
		src.Close()
		closeErr := dstF.Close()
		wrote += n
		if n > int64(budget) {
			os.RemoveAll(destDir)
			return fmt.Errorf("project: template package too large")
		}
		if cpErr != nil && cpErr != io.EOF {
			os.RemoveAll(destDir)
			return fmt.Errorf("project: extract %s: %w", f.Name, cpErr)
		}
		if closeErr != nil {
			os.RemoveAll(destDir)
			return closeErr
		}
	}
	return nil
}
