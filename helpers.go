// helpers.go
//
// helper functions and constants

package main

import (
	"bufio"
	"bytes"
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
	"time"

	"github.com/BourgeoisBear/rasterm"
	"github.com/PuerkitoBio/goquery"
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
func expandFilepaths(writer *outputWriter, p params) (expanded []*string, err error) {
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
	req.Header.Set("User-Agent", userAgent)

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
func openFilesForPrompt(
	promptFiles map[string][]byte,
	filepaths []*string,
) (files map[string]io.Reader, filesToClose []*os.File, err error) {
	files = map[string]io.Reader{}
	filesToClose = []*os.File{}

	i := 0
	for url, file := range promptFiles {
		files[fmt.Sprintf("%d_%s", i+1, url)] = bytes.NewReader(file)
		i++
	}
	for i, fp := range slices.DeleteFunc(
		filepaths,
		func(fp *string) bool { // skip nil
			return fp == nil
		},
	) {
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
	return
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
