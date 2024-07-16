// helpers.go

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"google.golang.org/api/googleapi"
)

const (
	// for replacing URLs in prompt to body texts
	urlRegexp       = `https?:\/\/(www\.)?[-a-zA-Z0-9@:%._\+~#=]{1,256}\.[a-zA-Z0-9()]{1,6}\b([-a-zA-Z0-9()@:%_\+.~#?&//=]*)`
	urlToTextFormat = "<link url=\"%[1]s\" content-type=\"%[2]s\">\n%[3]s\n</link>"
)

// convert error to string
func errorString(err error) (error string) {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return fmt.Sprintf("googleapi error: %s", gerr.Body)
	} else {
		return err.Error()
	}
}

// strip trailing charset text from given mime type
func stripCharsetFromMimeType(mimeType string) string {
	splitted := strings.Split(mimeType, ";")
	return splitted[0]
}

// replace all http urls in given text to body texts
func replaceHTTPURLsInPromptToBodyTexts(prompt string, verbose bool) string {
	re := regexp.MustCompile(urlRegexp)
	for _, url := range re.FindAllString(prompt, -1) {
		if converted, err := urlToText(url, verbose); err == nil {
			prompt = strings.Replace(prompt, url, fmt.Sprintf("%s\n", converted), 1)
		}
	}

	return prompt
}

// fetch the content from given url and convert it to text for prompting.
func urlToText(url string, verbose bool) (body string, err error) {
	client := &http.Client{
		Timeout: time.Duration(fetchURLTimeoutSeconds) * time.Second,
	}

	if verbose {
		log("[verbose] fetching from url: %s", url)
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch contents from url: %s", err)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

	if verbose {
		log("[verbose] fetched '%s' from url: %s", contentType, url)
	}

	if resp.StatusCode == 200 {
		if strings.HasPrefix(contentType, "text/html") {
			var doc *goquery.Document
			if doc, err = goquery.NewDocumentFromReader(resp.Body); err == nil {
				_ = doc.Find("script").Remove() // NOTE: removing unwanted javascripts

				body = fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(doc.Text()))
			} else {
				body = fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this HTML document.")
				err = fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err)
			}
		} else if strings.HasPrefix(contentType, "text/") {
			var bytes []byte
			if bytes, err = io.ReadAll(resp.Body); err == nil {
				body = fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(string(bytes))) // NOTE: removing redundant empty lines
			} else {
				body = fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document.")
				err = fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err)
			}
		} else if strings.HasPrefix(contentType, "application/json") {
			var bytes []byte
			if bytes, err = io.ReadAll(resp.Body); err == nil {
				body = fmt.Sprintf(urlToTextFormat, url, contentType, string(bytes))
			} else {
				body = fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document.")
				err = fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err)
			}
		} else {
			body = fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType))
			err = fmt.Errorf("content type '%s' not supported for url: %s", contentType, url)
		}
	} else {
		body = fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("HTTP Error %d", resp.StatusCode))
		err = fmt.Errorf("http error %d from url: %s", resp.StatusCode, url)
	}

	if verbose {
		log("[verbose] fetched body =\n%s\n", body)
	}

	return body, err
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

// get pointer of given value
func ptr[T any](v T) *T {
	val := v
	return &val
}

// print given strings to stdout
func log(format string, v ...any) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}

	fmt.Printf(format, v...)
}

// print given strings and exit with code
func logAndExit(code int, format string, v ...any) {
	log(format, v...)

	os.Exit(code)
}

// prettify given thing in JSON format
func prettify(v any) string {
	if bytes, err := json.MarshalIndent(v, "", "  "); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%+v", v)
}
