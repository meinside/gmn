// helpers.go

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/fatih/color"
	"github.com/jwalton/go-supportscolor"
)

const (
	// for replacing URLs in prompt to body texts
	urlRegexp       = `https?:\/\/(www\.)?[-a-zA-Z0-9@:%._\+~#=]{1,256}\.[a-zA-Z0-9()]{1,6}\b([-a-zA-Z0-9()@:%_\+.~#?&//=]*)`
	urlToTextFormat = "<link url=\"%[1]s\" content-type=\"%[2]s\">\n%[3]s\n</link>"
)

// replace all http urls in given text to body texts
func replaceURLsInPrompt(conf config, p params) (replaced string, files map[string][]byte) {
	userAgent := *p.UserAgent
	prompt := p.Prompt
	vb := p.Verbose

	files = map[string][]byte{}

	re := regexp.MustCompile(urlRegexp)
	for _, url := range re.FindAllString(prompt, -1) {
		if fetched, contentType, err := fetchContent(conf, userAgent, url, vb); err == nil {
			if supportedHTTPContentType(contentType) {
				logVerbose(verboseMaximum, vb, "text content (%s) fetched from '%s' is supported", contentType, url)

				// replace prompt text
				prompt = strings.Replace(prompt, url, fmt.Sprintf("%s\n", string(fetched)), 1)
			} else if supportedFileMimeType(contentType) {
				logVerbose(verboseMaximum, vb, "file content (%s) fetched from '%s' is supported", contentType, url)

				// replace prompt text,
				prompt = strings.Replace(prompt, url, fmt.Sprintf(urlToTextFormat, url, contentType, ""), 1)

				// and add bytes as a file
				files[url] = fetched
			} else {
				logVerbose(verboseMaximum, vb, "fetched content (%s) from '%s' is not supported", contentType, url)
			}
		} else {
			logVerbose(verboseMedium, vb, "failed to fetch content from '%s': %s", url, err)
		}
	}

	return prompt, files
}

// fetch the content from given url and convert it to text for prompting.
func fetchContent(conf config, userAgent, url string, vb []bool) (converted []byte, contentType string, err error) {
	client := &http.Client{
		Timeout: time.Duration(conf.ReplaceHTTPURLTimeoutSeconds) * time.Second,
	}

	logVerbose(verboseMaximum, vb, "fetching content from '%s'", url)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, contentType, fmt.Errorf("failed to create http request: %s", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, contentType, fmt.Errorf("failed to fetch contents from '%s': %s", url, err)
	}
	defer resp.Body.Close()

	contentType = resp.Header.Get("Content-Type")

	logVerbose(verboseMaximum, vb, "fetched content (%s) from '%s'", contentType, url)

	if resp.StatusCode == 200 {
		if supportedHTTPContentType(contentType) {
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
					err = fmt.Errorf("failed to read document (%s) from '%s': %s", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "text/") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					// (success)
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(string(bytes)))) // NOTE: removing redundant empty lines
				} else {
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document."))
					err = fmt.Errorf("failed to read document (%s) from '%s': %s", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "application/json") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, string(bytes)))
				} else {
					converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document."))
					err = fmt.Errorf("failed to read document (%s) from '%s': %s", contentType, url, err)
				}
			} else {
				converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType)))
				err = fmt.Errorf("content (%s) from '%s' not supported", contentType, url)
			}
		} else if supportedFileMimeType(contentType) {
			if converted, err = io.ReadAll(resp.Body); err != nil {
				converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this file."))
				err = fmt.Errorf("failed to read file (%s) from '%s': %s", contentType, url, err)
			}
		} else {
			converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType)))
			err = fmt.Errorf("content (%s) from '%s' not supported", contentType, url)
		}
	} else {
		converted = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("HTTP Error %d", resp.StatusCode)))
		err = fmt.Errorf("http error %d from '%s'", resp.StatusCode, url)
	}

	logVerbose(verboseMaximum, vb, "fetched body =\n%s", string(converted))

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

// check if given file's mime type is supported
//
// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#supported_file_formats
func supportedFileMimeType(mimeType string) bool {
	return func(mimeType string) bool {
		switch {
		// images
		//
		// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#image_formats
		case slices.Contains([]string{
			"image/png",
			"image/jpeg",
			"image/webp",
			"image/heic",
			"image/heif",
		}, mimeType):
			return true
		// audios
		//
		// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#audio_formats
		case slices.Contains([]string{
			"audio/wav",
			"audio/mp3",
			"audio/aiff",
			"audio/aac",
			"audio/ogg",
			"audio/flac",
		}, mimeType):
			return true
		// videos
		//
		// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#video_formats
		case slices.Contains([]string{
			"video/mp4",
			"video/mpeg",
			"video/mov",
			"video/avi",
			"video/x-flv",
			"video/mpg",
			"video/webm",
			"video/wmv",
			"video/3gpp",
		}, mimeType):
			return true
		// plain text formats
		//
		// https://ai.google.dev/gemini-api/docs/prompting_with_media?lang=go#plain_text_formats
		case slices.Contains([]string{
			"text/plain",
			"text/html",
			"text/css",
			"text/javascript",
			"application/x-javascript",
			"text/x-typescript",
			"application/x-typescript",
			"text/csv",
			"text/markdown",
			"text/x-python",
			"application/x-python-code",
			"application/json",
			"text/xml",
			"application/rtf",
			"text/rtf",

			// FIXME: not stated in the document yet
			"application/pdf",
		}, mimeType):
			return true
		default:
			return false
		}
	}(mimeType)
}

// check if given HTTP content type is supported
func supportedHTTPContentType(contentType string) bool {
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

// verbosity level constants
type verbosity uint

const (
	verboseNone    verbosity = iota
	verboseMinimum verbosity = iota
	verboseMedium  verbosity = iota
	verboseMaximum verbosity = iota
)

// check level of verbosity
func verboseLevel(verbose []bool) verbosity {
	if len(verbose) == 1 {
		return verboseMinimum
	} else if len(verbose) == 2 {
		return verboseMedium
	} else if len(verbose) >= 3 {
		return verboseMaximum
	}

	return verboseNone
}

// print given string to stdout
func logMessage(level verbosity, format string, v ...any) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}

	var c color.Attribute
	switch level {
	case verboseMinimum:
		c = color.FgGreen
	case verboseMedium, verboseMaximum:
		c = color.FgYellow
	default:
		c = color.FgWhite
	}

	if supportscolor.Stdout().SupportsColor { // if color is supported,
		c := color.New(c)
		c.Printf(format, v...)
	} else {
		fmt.Printf(format, v...)
	}
}

// print given error string to stdout
func logError(format string, v ...any) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}

	if supportscolor.Stdout().SupportsColor { // if color is supported,
		c := color.New(color.FgRed)
		c.Printf(format, v...)
	} else {
		fmt.Printf(format, v...)
	}
}

// print logVerbose message
//
// (only when the level of given `verbosityFromParams` is greater or equal to `targetLevel`)
func logVerbose(targetLevel verbosity, verbosityFromParams []bool, format string, v ...any) {
	if vb := verboseLevel(verbosityFromParams); vb >= targetLevel {
		format = fmt.Sprintf(">>> %s", format)

		logMessage(targetLevel, format, v...)
	}
}

// print given strings and exit with code
func logAndExit(code int, format string, v ...any) {
	logError(format, v...)

	os.Exit(code)
}

// prettify given thing in JSON format
func prettify(v any) string {
	if bytes, err := json.MarshalIndent(v, "", "  "); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%+v", v)
}
