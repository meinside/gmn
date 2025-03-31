// helpers.go
//
// helper functions and constants

package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BourgeoisBear/rasterm"
	"github.com/PuerkitoBio/goquery"
	"github.com/tailscale/hujson"

	gt "github.com/meinside/gemini-things-go"
)

const (
	// for replacing URLs in prompt to body texts
	urlRegexp       = `https?:\/\/(www\.)?[-a-zA-Z0-9@:%._\+~#=]{1,256}\.[a-zA-Z0-9()]{1,6}\b([-a-zA-Z0-9()@:%_\+.~#?&//=]*)`
	urlToTextFormat = "<link url=\"%[1]s\" content-type=\"%[2]s\">\n%[3]s\n</link>"
)

// file/directory names to ignore while recursing directories
var _namesToIgnore = []string{
	"/", // NOTE: ignore root
	".cache/",
	".config/",
	".DS_Store",
	".env",
	".env.local",
	".git/",
	".ssh/",
	".svn/",
	".Trash/",
	".venv/",
	"build/",
	"config.json", "config.toml", "config.yaml", "config.yml",
	"Thumbs.db",
	"dist/",
	"node_modules/",
	"target/",
	"tmp/",
}
var _fileNamesToIgnore, _dirNamesToIgnore map[string]bool

// initialize things
func init() {
	// files and directories' names to ignore
	_fileNamesToIgnore, _dirNamesToIgnore = map[string]bool{}, map[string]bool{}
	for _, name := range _namesToIgnore {
		if strings.HasSuffix(name, "/") {
			_dirNamesToIgnore[filepath.Dir(name)] = true
		} else {
			_fileNamesToIgnore[name] = true
		}
	}
}

// standardize given JSON (JWCC) bytes
func standardizeJSON(b []byte) ([]byte, error) {
	ast, err := hujson.Parse(b)
	if err != nil {
		return b, err
	}
	ast.Standardize()

	return ast.Pack(), nil
}

// check if given directory should be ignored
func ignoredDirectory(path string) bool {
	if _, exists := _dirNamesToIgnore[filepath.Base(path)]; exists {
		logMessage(verboseMedium, "Ignoring directory '%s'", path)
		return true
	}
	return false
}

// check if given file should be ignored
func ignoredFile(path string, stat os.FileInfo) bool {
	// ignore empty files,
	if stat.Size() <= 0 {
		logMessage(verboseMedium, "Ignoring empty file '%s'", path)
		return true
	}

	// ignore files with ignored names,
	if _, exists := _fileNamesToIgnore[filepath.Base(path)]; exists {
		logMessage(verboseMedium, "Ignoring file '%s'", path)
		return true
	}

	return false
}

// return all files' paths in the given directory
func filesInDir(dir string, vbs []bool) ([]*string, error) {
	var files []*string

	// traverse directory
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if d.IsDir() {
			if ignoredDirectory(path) {
				return filepath.SkipDir
			}
		} else {
			stat, err := os.Stat(path)
			if err != nil {
				return err
			}

			if ignoredFile(path, stat) {
				return nil
			}

			logVerbose(verboseMedium, vbs, "attaching file '%s'", path)

			files = append(files, &path)
		}

		return nil
	})

	return files, err
}

// expand given filepaths (expand directories with their sub files)
func expandFilepaths(p params) (expanded []*string, err error) {
	filepaths := p.Filepaths
	if filepaths == nil {
		return nil, nil
	}

	// expand directories with their sub files
	expanded = []*string{}
	for _, fp := range filepaths {
		if fp == nil {
			continue
		}

		if stat, err := os.Stat(*fp); err == nil {
			if stat.IsDir() {
				if files, err := filesInDir(*fp, p.Verbose); err == nil {
					expanded = append(expanded, files...)
				} else {
					return nil, fmt.Errorf("failed to list files in '%s': %w", *fp, err)
				}
			} else {
				if ignoredFile(*fp, stat) {
					continue
				}
				expanded = append(expanded, fp)
			}
		} else {
			return nil, err
		}
	}

	// filter filepaths by supported mime types
	filtered := []*string{}
	for _, fp := range expanded {
		if fp == nil {
			continue
		}

		if matched, supported, err := gt.SupportedMimeTypePath(*fp); err == nil {
			if supported {
				filtered = append(filtered, fp)
			} else {
				logMessage(verboseMedium, "Ignoring file '%s', unsupported mime type: %s", *fp, matched)
			}
		} else {
			return nil, fmt.Errorf("failed to check mime type of '%s': %w", *fp, err)
		}
	}

	// remove redundant paths
	filtered = uniqPtrs(filtered)

	logVerbose(verboseMedium, p.Verbose, "attaching %d unique file(s)", len(filtered))

	return filtered, nil
}

// replace all http urls in given text to body texts
func replaceURLsInPrompt(conf config, p params) (replaced string, files map[string][]byte) {
	userAgent := *p.UserAgent
	prompt := *p.Prompt
	vbs := p.Verbose

	files = map[string][]byte{}

	re := regexp.MustCompile(urlRegexp)
	for _, url := range re.FindAllString(prompt, -1) {
		if fetched, contentType, err := fetchContent(conf, userAgent, url, vbs); err == nil {
			if mimeType, supported, _ := gt.SupportedMimeType(fetched); supported { // if it is a file of supported types,
				logVerbose(verboseMaximum, vbs, "file content (%s) fetched from '%s' is supported", mimeType, url)

				// NOTE: embeedings is for text only for now
				if p.GenerateEmbeddings {
					// replace prompt text
					prompt = strings.Replace(prompt, url, fmt.Sprintf("%s\n", string(fetched)), 1)
				} else {
					// replace prompt text,
					prompt = strings.Replace(prompt, url, fmt.Sprintf(urlToTextFormat, url, mimeType, ""), 1)

					// and add bytes as a file
					files[url] = fetched
				}
			} else if supportedTextContentType(contentType) { // if it is a text of supported types,
				logVerbose(verboseMaximum, vbs, "text content (%s) fetched from '%s' is supported", contentType, url)

				// replace prompt text
				prompt = strings.Replace(prompt, url, fmt.Sprintf("%s\n", string(fetched)), 1)
			} else { // otherwise, (not supported in anyways)
				logVerbose(verboseMaximum, vbs, "fetched content (%s) from '%s' is not supported", contentType, url)
			}
		} else {
			logVerbose(verboseMedium, vbs, "failed to fetch content from '%s': %s", url, err)
		}
	}

	return prompt, files
}

// fetch the content from given url and convert it to text for prompting.
func fetchContent(conf config, userAgent, url string, vbs []bool) (converted []byte, contentType string, err error) {
	client := &http.Client{
		Timeout: time.Duration(conf.ReplaceHTTPURLTimeoutSeconds) * time.Second,
	}

	logVerbose(verboseMaximum, vbs, "fetching content from '%s'", url)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, contentType, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, contentType, fmt.Errorf("failed to fetch contents from '%s': %w", url, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logError("Failed to close response body: %s", err)
		}
	}()

	// NOTE: get the content type from the header, not inferencing from the body bytes
	contentType = resp.Header.Get("Content-Type")

	logVerbose(verboseMaximum, vbs, "fetched content (%s) from '%s'", contentType, url)

	if resp.StatusCode == 200 {
		if supportedTextContentType(contentType) {
			if strings.HasPrefix(contentType, "text/html") {
				var doc *goquery.Document
				if doc, err = goquery.NewDocumentFromReader(resp.Body); err == nil {
					// NOTE: removing unwanted things here
					_ = doc.Find("script").Remove()                   // javascripts
					_ = doc.Find("link[rel=\"stylesheet\"]").Remove() // css links
					_ = doc.Find("style").Remove()                    // embeded css tyles

					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(doc.Text())))
				} else {
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this HTML document."))
					err = fmt.Errorf("failed to read document (%s) from '%s': %w", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "text/") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(string(bytes)))) // NOTE: removing redundant empty lines
				} else {
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document."))
					err = fmt.Errorf("failed to read document (%s) from '%s': %w", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "application/json") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, string(bytes)))
				} else {
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document."))
					err = fmt.Errorf("failed to read document (%s) from '%s': %w", contentType, url, err)
				}
			} else {
				converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType)))
				err = fmt.Errorf("content (%s) from '%s' not supported", contentType, url)
			}
		} else {
			if converted, err = io.ReadAll(resp.Body); err == nil {
				if matched, supported, _ := gt.SupportedMimeType(converted); !supported {
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, matched, fmt.Sprintf("Content type '%s' not supported.", matched)))
					err = fmt.Errorf("content (%s) from '%s' not supported", matched, url)
				}
			} else {
				converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this file."))
				err = fmt.Errorf("failed to read file (%s) from '%s': %w", contentType, url, err)
			}
		}
	} else {
		converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("HTTP Error %d", resp.StatusCode)))
		err = fmt.Errorf("http error %d from '%s'", resp.StatusCode, url)
	}

	logVerbose(verboseMaximum, vbs, "fetched body =\n%s", string(converted))

	return converted, contentType, err
}

// remove consecutive empty lines for compacting prompt lines
func removeConsecutiveEmptyLines(input string) string {
	// trim each line
	trimmed := []string{}
	for _, line := range strings.Split(input, "\n") {
		trimmed = append(trimmed, strings.TrimRight(line, " "))
	}
	input = strings.Join(trimmed, "\n")

	// remove redundant empty lines
	regex := regexp.MustCompile("\n{2,}")
	return regex.ReplaceAllString(input, "\n")
}

// check if given HTTP content type is a supported text type
func supportedTextContentType(contentType string) bool {
	return func(contentType string) bool {
		switch {
		case strings.HasPrefix(contentType, "text/"):
			return true
		case strings.HasPrefix(contentType, "application/json"):
			return true
		default:
			return false
		}
	}(contentType)
}

// get pointer of given value
func ptr[T any](v T) *T {
	val := v
	return &val
}

// get unique elements of given slice of pointers
func uniqPtrs[T comparable](slice []*T) []*T {
	keys := map[T]bool{}
	list := []*T{}
	for _, entry := range slice {
		if _, value := keys[*entry]; !value {
			keys[*entry] = true
			list = append(list, entry)
		}
	}
	return list
}

// open and return files for prompt (`filesToClose` should be closed manually)
func openFilesForPrompt(promptFiles map[string][]byte, filepaths []*string) (files map[string]io.Reader, filesToClose []*os.File, err error) {
	files = map[string]io.Reader{}
	filesToClose = []*os.File{}

	i := 0
	for url, file := range promptFiles {
		files[fmt.Sprintf("%d_%s", i+1, url)] = bytes.NewReader(file)
		i++
	}
	for i, fp := range filepaths {
		if opened, err := os.Open(*fp); err == nil {
			files[fmt.Sprintf("%d_%s", i+1, filepath.Base(*fp))] = opened
			filesToClose = append(filesToClose, opened)
		} else {
			return nil, nil, err
		}
	}

	return files, filesToClose, nil
}

// print image to terminal which supports sixel
//
// (referenced: https://github.com/BourgeoisBear/rasterm/blob/main/rasterm_test.go)
func displayImageOnTerminal(imgBytes []byte, mimeType string) error {
	if rasterm.IsKittyCapable() { // kitty
		if strings.HasSuffix(mimeType, "png") {
			return rasterm.KittyCopyPNGInline(os.Stdout, bytes.NewBuffer(imgBytes), rasterm.KittyImgOpts{})
		} else {
			if img, _, err := image.Decode(bytes.NewBuffer(imgBytes)); err == nil {
				return rasterm.KittyWriteImage(os.Stdout, img, rasterm.KittyImgOpts{})
			} else {
				return fmt.Errorf("failed to decode %s: %w", mimeType, err)
			}
		}
	} else if rasterm.IsItermCapable() { // iTerm
		return rasterm.ItermCopyFileInline(os.Stdout, bytes.NewBuffer(imgBytes), int64(len(imgBytes)))
	} else { // sixel
		if img, _, err := image.Decode(bytes.NewBuffer(imgBytes)); err == nil {
			if paletted, ok := img.(*image.Paletted); ok {
				return rasterm.SixelWriteImage(os.Stdout, paletted)
			} else {
				return fmt.Errorf("not a paletted image")
			}
		} else {
			return fmt.Errorf("failed to decode %s: %w", mimeType, err)
		}
	}
}

// generate a filepath for given mime type
//
// ($TMPDIR will be used if `destDir` is nil)
func genFilepath(mimeType, category string, destDir *string) string {
	var ext string
	var exists bool
	if ext, exists = strings.CutPrefix(mimeType, category+"/"); !exists {
		ext = "bin"
	}

	var dir string
	if destDir == nil {
		dir = os.TempDir()
	} else {
		dir = expandDir(*destDir)
	}

	return filepath.Join(
		dir,
		fmt.Sprintf("%s_%s.%s", appName, strconv.FormatInt(time.Now().UTC().UnixNano(), 10), ext),
	)
}

// expand given directory
func expandDir(dir string) string {
	// handle `~`,
	if strings.HasPrefix(dir, "~/") {
		if homeDir, err := os.UserHomeDir(); err == nil {
			dir = strings.Replace(dir, "~", homeDir, 1)
		}
	}

	// clean,
	dir = filepath.Clean(dir)

	return dir
}
