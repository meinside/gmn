// helpers.go
//
// Helper functions and constants.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BourgeoisBear/rasterm"
	"github.com/PuerkitoBio/goquery"
	"github.com/gabriel-vasile/mimetype"
	"github.com/tailscale/hujson"
	"google.golang.org/genai"
	"mvdan.cc/sh/v3/syntax"

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
func ignoredDirectory(writer *outputWriter, path string) bool {
	if _, exists := _dirNamesToIgnore[filepath.Base(path)]; exists {
		writer.print(
			verboseMedium,
			"Ignoring directory '%s'",
			path,
		)
		return true
	}
	return false
}

// check if given file should be ignored
func ignoredFile(
	writer *outputWriter,
	path string,
	stat os.FileInfo,
) bool {
	// ignore empty files,
	if stat.Size() <= 0 {
		writer.print(
			verboseMedium,
			"Ignoring empty file '%s'",
			path,
		)
		return true
	}

	// ignore files with ignored names,
	if _, exists := _fileNamesToIgnore[filepath.Base(path)]; exists {
		writer.print(
			verboseMedium,
			"Ignoring file '%s'",
			path,
		)
		return true
	}

	return false
}

// return all files' paths in the given directory
func filesInDir(
	writer *outputWriter,
	dir string,
	vbs []bool,
) ([]*string, error) {
	var files []*string

	// traverse directory
	err := filepath.WalkDir(
		dir,
		func(path string, d os.DirEntry, err error) error {
			if d.IsDir() {
				if ignoredDirectory(writer, path) {
					return filepath.SkipDir
				}
			} else {
				stat, err := os.Stat(path)
				if err != nil {
					return err
				}

				if ignoredFile(writer, path, stat) {
					return nil
				}

				writer.verbose(
					verboseMedium,
					vbs,
					"attaching file '%s'",
					path,
				)

				files = append(files, &path)
			}

			return nil
		})

	return files, err
}

// expand given filepaths (expand directories with their sub files)
func expandFilepaths(
	writer *outputWriter,
	p params,
) (expanded []*string, err error) {
	filepaths := p.Generation.Filepaths
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
				if files, err := filesInDir(writer, *fp, p.Verbose); err == nil {
					expanded = append(expanded, files...)
				} else {
					return nil, fmt.Errorf(
						"failed to list files in '%s': %w",
						*fp,
						err,
					)
				}
			} else {
				if ignoredFile(writer, *fp, stat) {
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

		// if file has an overridden mime type,
		if override, exists := p.OverrideFileMIMEType[filepath.Ext(*fp)]; exists {
			filtered = append(filtered, fp)

			writer.print(
				verboseMedium,
				"Overriding mime type of file '%s': %s",
				*fp,
				override,
			)
		} else { // check mime type from file bytes
			if matched, supported, err := gt.SupportedMimeTypePath(*fp); err == nil {
				if supported {
					filtered = append(filtered, fp)
				} else {
					writer.print(
						verboseMedium,
						"Ignoring file '%s', unsupported mime type: %s",
						*fp,
						matched,
					)
				}
			} else {
				return nil, fmt.Errorf(
					"failed to check mime type of '%s': %w",
					*fp,
					err,
				)
			}
		}
	}

	// remove redundant paths
	filtered = uniqPtrs(filtered)

	writer.verbose(
		verboseMedium,
		p.Verbose,
		"attaching %d unique file(s)",
		len(filtered),
	)

	return filtered, nil
}

// check if given `url` is from YouTube
func isURLFromYoutube(url string) bool {
	return slices.ContainsFunc([]string{
		"www.youtube.com",
		"youtu.be",
	}, func(e string) bool {
		return strings.Contains(url, e)
	})
}

type customURLInPrompt string

const (
	customURLSeparator = ":///"

	customURLLink    string = "link"
	customURLYoutube string = "youtube"
)

func linkURLInPrompt(url string) customURLInPrompt {
	return customURLInPrompt(customURLLink + customURLSeparator + url)
}

func youtubeURLInPrompt(url string) customURLInPrompt {
	return customURLInPrompt(customURLYoutube + customURLSeparator + url)
}

func (u customURLInPrompt) isLink() bool {
	return strings.HasPrefix(string(u), customURLLink)
}

func (u customURLInPrompt) isYoutube() bool {
	return strings.HasPrefix(string(u), customURLYoutube)
}

func (u customURLInPrompt) url() string {
	splitted := strings.Split(string(u), customURLSeparator)
	if len(splitted) > 1 {
		return splitted[1]
	}
	return ""
}

// replace all http urls in given text to body texts
func replaceURLsInPrompt(
	writer *outputWriter,
	conf config,
	p params,
) (replaced string, files map[customURLInPrompt][]byte) {
	userAgent := *p.Generation.UserAgent
	prompt := *p.Generation.Prompt
	vbs := p.Verbose

	files = map[customURLInPrompt][]byte{}

	re := regexp.MustCompile(urlRegexp)
	for _, url := range re.FindAllString(prompt, -1) {
		// if `url` is from YouTube,
		if isURLFromYoutube(url) {
			files[youtubeURLInPrompt(url)] = []byte(url)

			// replace prompt text
			prompt = strings.Replace(
				prompt,
				url,
				fmt.Sprintf("<youtube url=\"%s\">", url),
				1,
			)
		} else {
			if fetched, contentType, err := fetchContent(
				writer,
				conf,
				userAgent,
				url,
				vbs,
			); err == nil {
				if mimeType, supported, _ := gt.SupportedMimeType(fetched); supported { // if it is a file of supported types,
					writer.verbose(
						verboseMaximum,
						vbs,
						"file content (%s) fetched from '%s' is supported",
						mimeType,
						url,
					)

					// NOTE: embeddings is for text only for now
					if p.Embeddings.GenerateEmbeddings {
						// replace prompt text
						prompt = strings.Replace(
							prompt,
							url,
							fmt.Sprintf("%s\n", string(fetched)),
							1,
						)
					} else {
						// replace prompt text,
						prompt = strings.Replace(
							prompt,
							url,
							fmt.Sprintf(urlToTextFormat, url, mimeType, ""),
							1,
						)

						// and add bytes as a file
						files[linkURLInPrompt(url)] = fetched
					}
				} else if supportedTextContentType(contentType) { // if it is a text of supported types,
					writer.verbose(
						verboseMaximum,
						vbs,
						"text content (%s) fetched from '%s' is supported",
						contentType,
						url,
					)

					// replace prompt text
					prompt = strings.Replace(
						prompt,
						url,
						fmt.Sprintf("%s\n", string(fetched)),
						1,
					)
				} else { // otherwise, (not supported in anyways)
					writer.verbose(
						verboseMaximum,
						vbs,
						"fetched content (%s) from '%s' is not supported",
						contentType,
						url,
					)
				}
			} else {
				writer.verbose(
					verboseMedium,
					vbs,
					"failed to fetch content from '%s': %s",
					url,
					err,
				)
			}
		}
	}

	return prompt, files
}

// check if there is any http url in given text prompt
func urlsInPrompt(p params) bool {
	return regexp.MustCompile(urlRegexp).
		FindAllString(*p.Generation.Prompt, -1) != nil
}

// fetch the content from given url and convert it to text for prompting.
func fetchContent(
	writer *outputWriter,
	conf config,
	userAgent,
	url string,
	vbs []bool,
) (converted []byte, contentType string, err error) {
	client := &http.Client{
		Timeout: time.Duration(conf.ReplaceHTTPURLTimeoutSeconds) * time.Second,
	}

	writer.verbose(
		verboseMaximum,
		vbs,
		"fetching content from '%s'",
		url,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, contentType, fmt.Errorf(
			"failed to create http request: %w",
			err,
		)
	}

	// request headers
	req.Header.Set(`User-Agent`, userAgent)
	req.Header.Set(`Sec-Fetch-Dest`, `document`)
	req.Header.Set(`Sec-Fetch-Mode`, `navigate`)
	req.Header.Set(`Sec-Fetch-Site`, `none`)
	req.Header.Set(`Sec-Fetch-User`, `?1`)

	resp, err := client.Do(req)
	if err != nil {
		return nil, contentType, fmt.Errorf(
			"failed to fetch contents from '%s': %w",
			url,
			err,
		)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			writer.error(
				"Failed to close response body: %s",
				err,
			)
		}
	}()

	// NOTE: get the content type from the header, not inferencing from the body bytes
	contentType = resp.Header.Get("Content-Type")

	writer.verbose(
		verboseMaximum,
		vbs,
		"fetched content (%s) from '%s'",
		contentType,
		url,
	)

	if resp.StatusCode == 200 {
		if supportedTextContentType(contentType) {
			if strings.HasPrefix(contentType, "text/html") {
				var doc *goquery.Document
				if doc, err = goquery.NewDocumentFromReader(resp.Body); err == nil {
					// NOTE: removing unwanted things here
					_ = doc.Find("script").Remove()                   // javascripts
					_ = doc.Find("link[rel=\"stylesheet\"]").Remove() // css links
					_ = doc.Find("style").Remove()                    // embeded css tyles

					converted = fmt.Appendf(
						nil,
						urlToTextFormat,
						url,
						contentType,
						removeConsecutiveEmptyLines(doc.Text()),
					)
				} else {
					converted = fmt.Appendf(
						nil,
						urlToTextFormat,
						url,
						contentType,
						"Failed to read this HTML document.",
					)
					err = fmt.Errorf(
						"failed to read document (%s) from '%s': %w",
						contentType,
						url,
						err,
					)
				}
			} else if strings.HasPrefix(contentType, "text/") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					converted = fmt.Appendf(
						nil,
						urlToTextFormat,
						url,
						contentType,
						removeConsecutiveEmptyLines(string(bytes)),
					) // NOTE: removing redundant empty lines
				} else {
					converted = fmt.Appendf(
						nil,
						urlToTextFormat,
						url,
						contentType,
						"Failed to read this document.",
					)
					err = fmt.Errorf(
						"failed to read document (%s) from '%s': %w",
						contentType,
						url,
						err,
					)
				}
			} else if strings.HasPrefix(contentType, "application/json") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					converted = fmt.Appendf(
						nil,
						urlToTextFormat,
						url,
						contentType,
						string(bytes),
					)
				} else {
					converted = fmt.Appendf(
						nil,
						urlToTextFormat,
						url,
						contentType,
						"Failed to read this document.",
					)
					err = fmt.Errorf(
						"failed to read document (%s) from '%s': %w",
						contentType,
						url,
						err,
					)
				}
			} else {
				converted = fmt.Appendf(
					nil,
					urlToTextFormat,
					url,
					contentType,
					fmt.Sprintf("Content type '%s' not supported.", contentType),
				)
				err = fmt.Errorf(
					"content (%s) from '%s' not supported",
					contentType,
					url,
				)
			}
		} else {
			if converted, err = io.ReadAll(resp.Body); err == nil {
				if matched, supported, _ := gt.SupportedMimeType(converted); !supported {
					converted = fmt.Appendf(
						nil,
						urlToTextFormat,
						url,
						matched,
						fmt.Sprintf("Content type '%s' not supported.", matched),
					)
					err = fmt.Errorf(
						"content (%s) from '%s' not supported",
						matched,
						url,
					)
				}
			} else {
				converted = fmt.Appendf(
					nil,
					urlToTextFormat,
					url,
					contentType,
					"Failed to read this file.",
				)
				err = fmt.Errorf(
					"failed to read file (%s) from '%s': %w",
					contentType,
					url,
					err,
				)
			}
		}
	} else {
		converted = fmt.Appendf(
			nil,
			urlToTextFormat,
			url, contentType,
			fmt.Sprintf("HTTP Error %d", resp.StatusCode),
		)
		err = fmt.Errorf(
			"http error %d from '%s'",
			resp.StatusCode,
			url,
		)
	}

	writer.verbose(
		verboseMaximum,
		vbs,
		"fetched body =\n%s",
		string(converted),
	)

	return converted, contentType, err
}

// remove consecutive empty lines for compacting prompt lines
func removeConsecutiveEmptyLines(input string) string {
	// trim each line
	trimmed := []string{}
	for line := range strings.SplitSeq(input, "\n") {
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

// convert given custom metadata to a map
func customMetadataToMap(metadata []*genai.CustomMetadata) map[string]string {
	m := map[string]string{}
	for _, meta := range metadata {
		if meta.StringValue != "" {
			m[meta.Key] = meta.StringValue
		} else if meta.NumericValue != nil {
			m[meta.Key] = fmt.Sprintf("%v", *meta.NumericValue)
		} else if meta.StringListValue != nil {
			m[meta.Key] = prettify(meta.StringListValue.Values, true)
		}
	}
	return m
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

// struct for opened file
type openedFile struct {
	filepath string
	reader   io.Reader
	closer   *os.File

	filename string
}

// Close closes this opened file.
func (f *openedFile) Close() error {
	if f.closer != nil {
		return f.closer.Close()
	}
	return nil
}

// open and return files for prompt (`files` should be closed manually)
func openFilesForPrompt(
	promptFiles map[string][]byte,
	filepaths []*string,
) (files []openedFile, err error) {
	files = []openedFile{}

	idx := 0
	for url, file := range promptFiles {
		files = append(files, openedFile{
			filepath: url,
			reader:   bytes.NewReader(file),
			filename: fmt.Sprintf("%d_%s", idx+1, url),
			closer:   nil,
		})
		idx++
	}
	for _, fp := range slices.DeleteFunc(
		filepaths,
		func(fp *string) bool { // skip nil
			return fp == nil
		},
	) {
		if opened, err := os.Open(*fp); err == nil {
			files = append(files, openedFile{
				filepath: *fp,
				reader:   opened,
				filename: fmt.Sprintf("%d_%s", idx+1, filepath.Base(*fp)),
				closer:   opened,
			})
		} else {
			return nil, err
		}
		idx++
	}

	return files, nil
}

// print image to terminal which supports sixel
//
// (referenced: https://github.com/BourgeoisBear/rasterm/blob/main/rasterm_test.go)
func displayImageOnTerminal(imgBytes []byte, mimeType string) error {
	if rasterm.IsKittyCapable() { // kitty
		if strings.HasSuffix(mimeType, "png") {
			return rasterm.KittyCopyPNGInline(
				os.Stdout,
				bytes.NewBuffer(imgBytes),
				rasterm.KittyImgOpts{},
			)
		} else {
			if img, _, err := image.Decode(bytes.NewBuffer(imgBytes)); err == nil {
				return rasterm.KittyWriteImage(
					os.Stdout,
					img,
					rasterm.KittyImgOpts{},
				)
			} else {
				return fmt.Errorf(
					"failed to decode %s: %w",
					mimeType,
					err,
				)
			}
		}
	} else if rasterm.IsItermCapable() { // iTerm
		return rasterm.ItermCopyFileInline(
			os.Stdout,
			bytes.NewBuffer(imgBytes),
			int64(len(imgBytes)),
		)
	} else { // sixel
		if img, _, err := image.Decode(bytes.NewBuffer(imgBytes)); err == nil {
			if paletted, ok := img.(*image.Paletted); ok {
				return rasterm.SixelWriteImage(os.Stdout, paletted)
			} else {
				return fmt.Errorf("not a paletted image")
			}
		} else {
			return fmt.Errorf(
				"failed to decode %s: %w",
				mimeType,
				err,
			)
		}
	}
}

// generate a filepath for given mime type
//
// ($TMPDIR will be used if `destDir` is nil)
func genFilepath(
	mimeType,
	category string,
	destDir *string,
) string {
	var ext string
	var exists bool
	if ext, exists = strings.CutPrefix(
		mimeType,
		category+"/",
	); !exists {
		ext = "bin"
	}
	ext = strings.Split(ext, ";")[0]

	var dir string
	if destDir == nil {
		dir = os.TempDir()
	} else {
		dir = expandPath(*destDir)
	}

	return filepath.Join(
		dir,
		fmt.Sprintf(
			"%s_%s.%s",
			appName,
			strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
			ext,
		),
	)
}

// expand given path
func expandPath(path string) string {
	// handle `~/*`,
	if strings.HasPrefix(path, "~/") {
		if homeDir, err := os.UserHomeDir(); err == nil {
			path = strings.Replace(
				path,
				"~",
				homeDir,
				1,
			)
		}
	}

	// handle `./*`,
	if strings.HasPrefix(path, "./") {
		if cwd, err := os.Getwd(); err == nil {
			path = strings.Replace(
				path,
				"./",
				cwd+"/",
				1,
			)
		}
	}

	// expand environment variables, eg. $HOME
	path = os.ExpandEnv(path)

	// clean,
	path = filepath.Clean(path)

	return path
}

// get speech codec and bit rate from mime type
func speechCodecAndBitRateFromMimeType(mimeType string) (
	speechCodec string,
	bitRate int,
) {
	for split := range strings.SplitSeq(mimeType, ";") {
		if strings.HasPrefix(split, "codec=") {
			speechCodec = split[6:]
		} else if strings.HasPrefix(split, "rate=") {
			bitRate, _ = strconv.Atoi(split[5:])
		}
	}
	return speechCodec, bitRate
}

// wav parameter constants
const (
	wavBitDepth    = 16
	wavNumChannels = 1
)

// convert pcm data to wav
func pcmToWav(
	data []byte,
	sampleRate int,
) (converted []byte, err error) {
	var buf bytes.Buffer

	// wav header
	dataLen := uint32(len(data))
	header := struct {
		ChunkID       [4]byte // "RIFF"
		ChunkSize     uint32
		Format        [4]byte // "WAVE"
		Subchunk1ID   [4]byte // "fmt "
		Subchunk1Size uint32
		AudioFormat   uint16
		NumChannels   uint16
		SampleRate    uint32
		ByteRate      uint32
		BlockAlign    uint16
		BitsPerSample uint16
		Subchunk2ID   [4]byte // "data"
		Subchunk2Size uint32
	}{
		ChunkID:       [4]byte{'R', 'I', 'F', 'F'},
		ChunkSize:     36 + dataLen,
		Format:        [4]byte{'W', 'A', 'V', 'E'},
		Subchunk1ID:   [4]byte{'f', 'm', 't', ' '},
		Subchunk1Size: 16,
		AudioFormat:   1, // PCM
		NumChannels:   uint16(wavNumChannels),
		SampleRate:    uint32(sampleRate),
		ByteRate:      uint32(sampleRate * wavNumChannels * wavBitDepth / 8),
		BlockAlign:    uint16(wavNumChannels * wavBitDepth / 8),
		BitsPerSample: uint16(wavBitDepth),
		Subchunk2ID:   [4]byte{'d', 'a', 't', 'a'},
		Subchunk2Size: dataLen,
	}

	// write wav header
	if err := binary.Write(
		&buf,
		binary.LittleEndian,
		header,
	); err != nil {
		return nil, fmt.Errorf(
			"failed to write wav header: %w",
			err,
		)
	}

	// write pcm data
	if _, err := buf.Write(data); err != nil {
		return nil, fmt.Errorf(
			"failed to write pcm data: %w",
			err,
		)
	}

	return buf.Bytes(), nil
}

// read mime type from a `io.Reader` and return a new one for recycling it
//
// (copied from `gemini-things-go`)
func readMimeAndRecycle(input io.Reader) (mimeType *mimetype.MIME, recycled io.Reader, err error) {
	// header will store the bytes mimetype uses for detection.
	header := bytes.NewBuffer(nil)

	// After DetectReader, the data read from input is copied into header.
	mtype, err := mimetype.DetectReader(io.TeeReader(input, header))
	if err != nil {
		return
	}

	// Concatenate back the header to the rest of the file.
	// recycled now contains the complete, original data.
	recycled = io.MultiReader(header, input)

	return mtype, recycled, err
}

// read bytes and  mime type from a `io.Reader` and return a new one for recycling it
//
// (altered `readMimeAndRecycle` above)
func readAndRecycle(input io.Reader) (mimeType *mimetype.MIME, file []byte, recycled io.Reader, err error) {
	// header will store the bytes mimetype uses for detection.
	header := bytes.NewBuffer(nil)

	file, err = io.ReadAll(io.TeeReader(input, header))
	if err != nil {
		return
	}

	mtype := mimetype.Detect(file)

	// Concatenate back the header to the rest of the file.
	// recycled now contains the complete, original data.
	recycled = io.MultiReader(header, input)

	return mtype, file, recycled, err
}

// run executable with given args and return its result
func runExecutable(
	execPath string,
	args map[string]any,
) (result string, err error) {
	execPath = expandPath(execPath)

	// marshal args
	var paramArgs []byte
	paramArgs, err = json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf(
			"failed to marshal args: %w",
			err,
		)
	}

	// and run
	arg := string(paramArgs)
	cmd := exec.Command(execPath, arg)
	var output []byte
	output, err = cmd.Output()
	if err != nil {
		return "", fmt.Errorf(
			"failed to run '%s' with args %s: %w",
			execPath,
			arg,
			err,
		)
	}

	return string(output), nil
}

// confirm with the given prompt (y/n)
func confirm(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("%s (y/N): ", prompt)

		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintln(
				os.Stderr,
				"Error reading input:",
				err,
			)
			continue
		}
		response = strings.ToLower(strings.TrimSpace(response))
		if strings.HasPrefix(response, "y") {
			return true
		} else {
			return false
		}
	}
}

// read user input from stdin
func readFromStdin(prompt string) (string, error) {
	fmt.Printf("%s: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	return reader.ReadString('\n')
}

// check if the past generations end with users's message,
func historyEndsWithUsers(history []genai.Content) bool {
	if len(history) > 0 {
		last := history[len(history)-1]

		return last.Role == string(gt.RoleUser)
	}
	return false
}

// check if there is any duplicated value between given arrays
func duplicated[V comparable](arrs ...[]V) (value V, duplicated bool) {
	pool := map[V]struct{}{}
	for _, arr := range arrs {
		for _, v := range arr {
			if _, exists := pool[v]; exists {
				return v, true
			}
			pool[v] = struct{}{}
		}
	}
	var zero V
	return zero, false
}

// parse commandline
func parseCommandline(cmdline string) (command string, args []string, err error) {
	parser := syntax.NewParser()

	var node *syntax.File
	if node, err = parser.Parse(strings.NewReader(cmdline), ""); err == nil {
		var parts []string
		syntax.Walk(node, func(node syntax.Node) bool {
			switch x := node.(type) {
			case *syntax.CallExpr:
				printer := syntax.NewPrinter()
				for _, word := range x.Args {
					var buf bytes.Buffer
					if err := printer.Print(&buf, word); err != nil {
						log.Printf("failure while serializing command line: %s", err)
						continue
					}
					parts = append(parts, buf.String())
				}
				return false
			}
			return true
		})

		if len(parts) > 0 {
			return parts[0], parts[1:], nil
		} else {
			err = fmt.Errorf("there was no available command or arguments from the command line")
		}
	} else {
		err = fmt.Errorf("failed to parse command line: %w", err)
	}

	return cmdline, nil, err
}

// prettify given thing in JSON format
func prettify(v any, flatten ...bool) string {
	if len(flatten) > 0 && flatten[0] {
		if bytes, err := json.Marshal(v); err == nil {
			return string(bytes)
		}
	} else {
		if bytes, err := json.MarshalIndent(v, "", "  "); err == nil {
			return string(bytes)
		}
	}
	return fmt.Sprintf("%+v", v)
}

// unmarshalJSONFromBytes handles standardizing and unmarshaling JSON bytes.
func unmarshalJSONFromBytes(data *string, target any) error {
	if data == nil {
		return nil // No data to unmarshal
	}
	bytes, err := standardizeJSON([]byte(*data))
	if err != nil {
		return fmt.Errorf("failed to standardize JSON: %w", err)
	}
	if err := json.Unmarshal(bytes, target); err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	return nil
}

type fileInfo struct {
	Name         string      `json:"filename"`
	AbsolutePath string      `json:"absolutePath"`
	Size         int64       `json:"filesize"`
	Mode         os.FileMode `json:"mode"`
	Modified     time.Time   `json:"modified"`
	IsDirectory  bool        `json:"isDirectory"`
	Sys          string      `json:"sys"`
}

// fileInfoToStruct converts os.FileInfo to struct.
func fileInfoToStruct(
	info os.FileInfo,
	absoluteFilepath string,
) fileInfo {
	return fileInfo{
		Name:         info.Name(),
		AbsolutePath: absoluteFilepath,
		Size:         info.Size(),
		Mode:         info.Mode(),
		Modified:     info.ModTime(),
		IsDirectory:  info.IsDir(),
		Sys:          fmt.Sprintf("%#v", info.Sys()),
	}
}

// fileInfoToJSON converts os.FileInfo to JSON string.
func fileInfoToJSON(
	info os.FileInfo,
	absoluteFilepath string,
) string {
	result := fileInfoToStruct(info, absoluteFilepath)

	if marshalled, err := json.Marshal(result); err == nil {
		return string(marshalled)
	} else {
		return fmt.Sprintf("%+v", result)
	}
}

type dirEntry struct {
	Name        string      `json:"filename"`
	IsDirectory bool        `json:"isDirectory"`
	Mode        os.FileMode `json:"mode"`

	Info *fileInfo `json:"info,omitempty"`
}

// dirEntryToStruct converts os.DirEntry to struct.
func dirEntryToStruct(
	entry os.DirEntry,
	parentDirpath string,
) dirEntry {
	result := dirEntry{
		Name:        entry.Name(),
		IsDirectory: entry.IsDir(),
		Mode:        entry.Type(),
	}
	if info, _ := entry.Info(); info != nil { // may be nil when entry was removed after listing dir entries
		result.Info = ptr(fileInfoToStruct(info, filepath.Join(parentDirpath, entry.Name())))
	}

	return result
}

// dirEntriesToJSON converts os.DirEntry to JSON string.
func dirEntriesToJSON(
	entries []os.DirEntry,
	parentDirpath string,
) string {
	result := []dirEntry{}
	for _, entry := range entries {
		result = append(result, dirEntryToStruct(entry, parentDirpath))
	}

	if marshalled, err := json.Marshal(struct {
		Result []dirEntry `json:"result"`
	}{
		Result: result,
	}); err == nil {
		return string(marshalled)
	} else {
		return fmt.Sprintf("%+v", result)
	}
}

// runCommandWithContext runs the given command + args with context.
func runCommandWithContext(
	ctx context.Context,
	command string,
	args ...string,
) (stdout, stderr string, exitCode int, err error) {
	cmd := exec.CommandContext(ctx, command, args...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()

	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()
	exitCode = 0

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			} else {
				exitCode = 1
				stderr += fmt.Sprintf("\n(Failed to get specific exit status, error: %v)\n", err)
			}
		} else {
			exitCode = 1
			stderr += fmt.Sprintf("\n(Command failed with non-ExitError: %v)\n", err)
		}
	}

	return stdout, stderr, exitCode, err
}

// helper function for creating a gemini-things client
// with gemini api key, or google credentials file
func gtClient(
	conf config,
	options ...gt.ClientOption,
) (gtc *gt.Client, err error) {
	err = fmt.Errorf("gemini api key or google credentials not found")

	if conf.GoogleAIAPIKey != nil {
		return gt.NewClient(
			*conf.GoogleAIAPIKey,
			options...,
		)
	} else if conf.GoogleCredentialsFilepath != nil {
		var credentialsBytes []byte
		if credentialsBytes, err = os.ReadFile(expandPath(*conf.GoogleCredentialsFilepath)); err != nil {
			return nil, fmt.Errorf("failed to read google credentials from %s: %w", *conf.GoogleCredentialsFilepath, err)
		}
		return gt.NewVertexClient(
			context.TODO(),
			credentialsBytes,
			*conf.Location,
			*conf.GoogleCloudStorageBucketNameForFileUploads,
			options...,
		)
	}

	return nil, fmt.Errorf("failed to create gemini-things client: %w", err)
}

// helper function for returning the first text, image, and video from given prompts
func promptImageOrVideoFromPrompts(writer *outputWriter, prompts []gt.Prompt) (prompt *string, image *genai.Image, video *genai.Video) {
	for i, p := range prompts {
		switch p := p.(type) { // FIXME: currently ignoring forced mime types
		case gt.TextPrompt:
			if prompt == nil {
				prompt = &p.Text
			}
		case gt.FilePrompt:
			if p.Data != nil {
				if strings.HasPrefix(p.Data.MIMEType, "image/") {
					if image == nil {
						image = &genai.Image{
							GCSURI:   p.Data.FileURI,
							MIMEType: p.Data.MIMEType,
						}
					} else {
						writer.warn(
							"Ignoring image at prompts[%d], because only 1 image is allowed.",
							i,
						)
					}
				} else if strings.HasPrefix(p.Data.MIMEType, "video/") {
					if video == nil {
						video = &genai.Video{
							URI:      p.Data.FileURI,
							MIMEType: p.Data.MIMEType,
						}
					} else {
						writer.warn(
							"Ignoring video at prompts[%d], because only 1 video is allowed.",
							i,
						)
					}
				}
			} else {
				if mime, bytes, recycled, err := readAndRecycle(p.Reader); err == nil {
					mimeType := mime.String()

					p.Reader = recycled
					prompts[i] = p

					if strings.HasPrefix(mimeType, "image/") {
						if image == nil {
							image = &genai.Image{
								ImageBytes: bytes,
								MIMEType:   mimeType,
							}
						} else {
							writer.warn(
								"Ignoring image at prompts[%d], because only 1 image is allowed.",
								i,
							)
						}
					} else if strings.HasPrefix(mimeType, "video/") {
						if video == nil {
							video = &genai.Video{
								VideoBytes: bytes,
								MIMEType:   mimeType,
							}
						} else {
							writer.warn(
								"Ignoring video at prompts[%d], because only 1 video is allowed.",
							)
						}
					}
				} else {
					writer.warn(
						"Failed to read file at prompts[%d]: %s",
						i,
						err,
					)
				}
			}
		case gt.URIPrompt:
			if strings.HasPrefix(p.MIMEType, "image/") {
				if image == nil {
					image = &genai.Image{
						GCSURI:   p.URI,
						MIMEType: p.MIMEType,
					}
				} else {
					writer.warn(
						"Ignoring image at prompts[%d], because only 1 image is allowed.",
						i,
					)
				}
			} else if strings.HasPrefix(p.MIMEType, "video/") {
				if video == nil {
					video = &genai.Video{
						URI:      p.URI,
						MIMEType: p.MIMEType,
					}
				} else {
					writer.warn(
						"Ignoring video at prompts[%d], because only 1 video is allowed.",
						i,
					)
				}
			}
		case gt.BytesPrompt:
			if strings.HasPrefix(p.MIMEType, "image/") {
				if image == nil {
					image = &genai.Image{
						ImageBytes: p.Bytes,
						MIMEType:   p.MIMEType,
					}
				} else {
					writer.warn(
						"Ignoring image at prompts[%d], because only 1 image is allowed.",
						i,
					)
				}
			} else if strings.HasPrefix(p.MIMEType, "video/") {
				if video == nil {
					video = &genai.Video{
						VideoBytes: p.Bytes,
						MIMEType:   p.MIMEType,
					}
				} else {
					writer.warn(
						"Ignoring video at prompts[%d], because only 1 video is allowed.",
						i,
					)
				}
			}
		}
	}

	return prompt, image, video
}

// generate safety settings for the client
//
// NOTE: all possible categories will be turned off
func safetySettings(clientType genai.Backend) []*genai.SafetySetting {
	settings := []*genai.SafetySetting{}

	var categories []genai.HarmCategory
	switch clientType {
	case genai.BackendGeminiAPI:
		categories = []genai.HarmCategory{
			genai.HarmCategoryHarassment,
			genai.HarmCategoryHateSpeech,
			genai.HarmCategorySexuallyExplicit,
			genai.HarmCategoryDangerousContent,
		}
	case genai.BackendVertexAI:
		categories = []genai.HarmCategory{
			genai.HarmCategoryHarassment,
			genai.HarmCategoryHateSpeech,
			genai.HarmCategorySexuallyExplicit,
			genai.HarmCategoryDangerousContent,
			genai.HarmCategoryImageHate,
			genai.HarmCategoryImageDangerousContent,
			genai.HarmCategoryImageHarassment,
			genai.HarmCategoryImageSexuallyExplicit,
			genai.HarmCategoryJailbreak,
		}
	}

	for _, category := range categories {
		settings = append(settings, &genai.SafetySetting{
			Category:  category,
			Threshold: genai.HarmBlockThresholdOff,
		})
	}

	return settings
}
