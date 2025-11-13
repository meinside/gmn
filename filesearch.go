// filesearch.go

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fatih/color"
	"github.com/gabriel-vasile/mimetype"
	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
)

// list file search stores
func listFileSearchStores(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"listing file search stores...",
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			writer.error(
				"Failed to close client: %s",
				err,
			)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	numStores := 0
	for store, err := range gtc.ListFileSearchStores(ctx) {
		if err != nil {
			return 1, err
		}

		writer.printColored(
			color.FgHiGreen,
			"%s",
			store.Name,
		)
		writer.printColored(
			color.FgHiWhite,
			` (%s)`,
			store.DisplayName,
		)

		writer.printColored(
			color.FgWhite,
			`
  > created: %s
  > updated: %s
  > active documents:  %d
  > pending documents: %d
  > failed documents:  %d
  > size: %d bytes
`,
			store.CreateTime.Format(time.RFC3339),
			store.UpdateTime.Format(time.RFC3339),
			store.ActiveDocumentsCount,
			store.PendingDocumentsCount,
			store.FailedDocumentsCount,
			store.SizeBytes,
		)

		numStores++
	}

	if numStores <= 0 {
		return 1, fmt.Errorf("no file search stores")
	}

	// success
	return 0, nil
}

// create a file search store
func createFileSearchStore(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	displayName string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"creating a file search store '%s'...",
		displayName,
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			writer.error(
				"Failed to close client: %s",
				err,
			)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	if created, err := gtc.CreateFileSearchStore(ctx, displayName); err != nil {
		return 1, err
	} else {
		writer.printColored(
			color.FgHiWhite,
			"%s",
			created.Name,
		)
	}

	// success
	return 0, nil
}

// delete a file search store
func deleteFileSearchStore(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	name string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"deleting a file search store '%s'...",
		name,
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			writer.error(
				"Failed to close client: %s",
				err,
			)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	if err := gtc.DeleteFileSearchStore(ctx, name); err != nil {
		return 1, err
	} else {
		writer.printColored(
			color.FgWhite,
			"Deleted file search store: ",
		)
		writer.printColored(
			color.FgHiWhite,
			"%s\n",
			name,
		)
	}

	// success
	return 0, nil
}

// upload files to file search store
func uploadFilesToFileSearchStore(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	fileSearchStoreName string,
	filepaths []string,
	chunkSize, overlappedChunkSize *uint,
	inferMIMETypeFromFileExtension bool,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"uploading files to file search store '%s'...",
		fileSearchStoreName,
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			writer.error(
				"Failed to close client: %s",
				err,
			)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	// chunk config
	var chunkConfig *genai.ChunkingConfig = nil
	if chunkSize != nil {
		chunkConfig = &genai.ChunkingConfig{
			WhiteSpaceConfig: &genai.WhiteSpaceConfig{},
		}

		chunkConfig.WhiteSpaceConfig.MaxTokensPerChunk = ptr(int32(*chunkSize))
	}
	if overlappedChunkSize != nil {
		if chunkConfig == nil {
			chunkConfig = &genai.ChunkingConfig{
				WhiteSpaceConfig: &genai.WhiteSpaceConfig{},
			}
		}

		chunkConfig.WhiteSpaceConfig.MaxOverlapTokens = ptr(int32(*overlappedChunkSize))
	}

	for _, path := range filepaths {
		if file, err := os.Open(path); err == nil {
			defer func() { _ = file.Close() }()

			var mimeType []string = nil
			if inferMIMETypeFromFileExtension {
				if inferMIMETypeFromFileExtension {
					mime, _ := mimetype.DetectFile(path)
					mimeType = []string{
						mime.String(),
					}
				}
			}

			if _, err := gtc.UploadFileForSearch(
				ctx,
				fileSearchStoreName,
				file,
				filepath.Base(path),
				[]*genai.CustomMetadata{
					{
						Key:         "filename",
						StringValue: path,
					},
				},
				chunkConfig,
				mimeType...,
			); err != nil {
				return 1, fmt.Errorf(
					"failed to upload file '%s' to file search store %s: %s",
					path,
					fileSearchStoreName,
					gt.ErrToStr(err),
				)
			} else {
				writer.printColored(
					color.FgWhite,
					"Uploaded '",
				)
				writer.printColored(
					color.FgHiWhite,
					"%s",
					path,
				)
				writer.printColored(
					color.FgWhite,
					"' to file search store: ",
				)
				writer.printColored(
					color.FgHiWhite,
					"%s\n",
					fileSearchStoreName,
				)
			}
		} else {
			return 1, err
		}
	}

	return 0, nil
}

// list files in a file search store
func listFilesInFileSearchStore(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	fileSearchStoreName string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"listing files in file search store '%s'...",
		fileSearchStoreName,
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			writer.error(
				"Failed to close client: %s",
				err,
			)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	numFiles := 0
	for file, err := range gtc.ListFilesInFileSearchStore(
		ctx,
		fileSearchStoreName,
	) {
		if err != nil {
			return 1, err
		}

		writer.printColored(
			color.FgHiGreen,
			"%s",
			file.Name,
		)
		writer.printColored(
			color.FgHiWhite,
			` %s`,
			file.DisplayName,
		)

		writer.printColored(
			color.FgWhite,
			`
  > created: %s
  > updated: %s
  > size: %d bytes (mimetype: %s)
  > metadata: %s
`,
			file.CreateTime.Format(time.RFC3339),
			file.UpdateTime.Format(time.RFC3339),
			file.SizeBytes,
			file.MIMEType,
			prettify(file.CustomMetadata, true),
		)

		numFiles++
	}

	if numFiles <= 0 {
		return 1, fmt.Errorf("no file in file search store '%s'", fileSearchStoreName)
	}

	// success
	return 0, nil
}

// delete a file in a file search store
func deleteFileInFileSearchStore(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	fileName string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"deleting a file '%s' in a file search store...",
		fileName,
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(apiKey)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			writer.error(
				"Failed to close client: %s",
				err,
			)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	if err := gtc.DeleteFileInFileSearchStore(ctx, fileName); err != nil {
		return 1, err
	} else {
		writer.printColored(
			color.FgWhite,
			"Deleted file '",
		)
		writer.printColored(
			color.FgHiWhite,
			"%s",
			fileName,
		)
		writer.printColored(
			color.FgWhite,
			"' in file search store\n",
		)
	}

	// success
	return 0, nil
}
