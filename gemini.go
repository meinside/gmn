// gemini.go

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	gt "github.com/meinside/gemini-things-go"
)

// generate with given things (will `os.Exit(0)` on success, or `os.Exit(1)` on any error)
func doGeneration(ctx context.Context, timeoutSeconds int, googleAIAPIKey, googleAIModel, systemInstruction, prompt string, promptFiles map[string][]byte, filepaths []*string, cachedContextName *string, vb []bool) {
	logVerbose(verboseMedium, vb, "generating...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIModel, googleAIAPIKey)
	if err != nil {
		logAndExit(1, "Failed to initialize client: %s", err)
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeout(timeoutSeconds)
	gtc.SetSystemInstructionFunc(func() string {
		return systemInstruction
	})

	// read & close files
	files := []io.Reader{}
	filesToClose := []*os.File{}
	for _, file := range promptFiles {
		files = append(files, bytes.NewReader(file))
	}
	for _, fp := range filepaths {
		if opened, err := os.Open(*fp); err == nil {
			files = append(files, opened)
			filesToClose = append(filesToClose, opened)
		} else {
			logAndExit(1, "Failed to open file: %s", err)
		}
	}
	defer func() {
		for _, toClose := range filesToClose {
			if err := toClose.Close(); err != nil {
				logError("Failed to close file: %s", err)
			}
		}
	}()

	// generation options
	opts := &gt.GenerationOptions{}
	if cachedContextName != nil {
		name := strings.TrimSpace(*cachedContextName)
		opts.CachedContextName = &name
	}

	// generate
	if err := gtc.GenerateStreamed(
		ctx,
		prompt,
		files,
		func(data gt.StreamCallbackData) {
			if data.TextDelta != nil {
				fmt.Print(*data.TextDelta)
			} else if data.NumTokens != nil {
				fmt.Print("\n") // FIXME: append a new line to the end of generated output

				// print the number of tokens
				logVerbose(verboseMinimum, vb, "input tokens: %d / output tokens: %d", data.NumTokens.Input, data.NumTokens.Output)

				// success
				os.Exit(0)
			} else if data.Error != nil {
				logAndExit(1, "Streaming failed: %s", gt.ErrToStr(data.Error))
			}
		},
		opts,
	); err != nil {
		logAndExit(1, "Generation failed: %s", gt.ErrToStr(err))
	}
}

// cache context (will `os.Exit(0)` on success, or `os.Exit(1)` on any error)
func cacheContext(ctx context.Context, timeoutSeconds int, googleAIAPIKey, googleAIModel, systemInstruction string, prompt *string, promptFiles map[string][]byte, filepaths []*string, cachedContextDisplayName *string, vb []bool) {
	logVerbose(verboseMedium, vb, "caching context...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIModel, googleAIAPIKey)
	if err != nil {
		logAndExit(1, "Failed to initialize client: %s", err)
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeout(timeoutSeconds)
	gtc.SetSystemInstructionFunc(func() string {
		return systemInstruction
	})

	// read & close files
	files := []io.Reader{}
	filesToClose := []*os.File{}
	for _, file := range promptFiles {
		files = append(files, bytes.NewReader(file))
	}
	for _, fp := range filepaths {
		if opened, err := os.Open(*fp); err == nil {
			files = append(files, opened)
			filesToClose = append(filesToClose, opened)
		} else {
			logAndExit(1, "Failed to open file: %s", err)
		}
	}
	defer func() {
		for _, toClose := range filesToClose {
			if err := toClose.Close(); err != nil {
				logError("Failed to close file: %s", err)
			}
		}
	}()

	// cache context and print the cached context's name
	if name, err := gtc.CacheContext(ctx, &systemInstruction, prompt, files, nil, nil, cachedContextDisplayName); err == nil {
		fmt.Print(name)
	} else {
		logError("Failed to cache context: %s", err)
	}
}

// list cached contexts (will `os.Exit(0)` on success, or `os.Exit(1)` on any error)
func listCachedContexts(ctx context.Context, timeoutSeconds int, googleAIAPIKey, googleAIModel string, vb []bool) {
	logVerbose(verboseMedium, vb, "listing cached contexts...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIModel, googleAIAPIKey)
	if err != nil {
		logAndExit(1, "Failed to initialize client: %s", err)
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	if listed, err := gtc.ListAllCachedContexts(ctx); err == nil {
		if len(listed) > 0 {
			fmt.Printf("%-28s  %-28s %-20s %s\n", "Name", "Model", "Expires", "Display Name")

			for _, content := range listed {
				fmt.Printf("%-28s  %-28s %-20s %s\n",
					content.Name,
					content.Model,
					content.Expiration.ExpireTime.Format("2006-01-02 15:04 MST"),
					content.DisplayName)
			}
		} else {
			logAndExit(1, "No cached contexts.")
		}
	} else {
		logAndExit(1, "Failed to list cached contexts: %s", err)
	}

	// success
	os.Exit(0)
}

// delete cached context (will `os.Exit(0)` on success, or `os.Exit(1)` on any error)
func deleteCachedContext(ctx context.Context, timeoutSeconds int, googleAIAPIKey, googleAIModel string, cachedContextName string, vb []bool) {
	logVerbose(verboseMedium, vb, "deleting cached context...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIModel, googleAIAPIKey)
	if err != nil {
		logAndExit(1, "Failed to initialize client: %s", err)
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	if err := gtc.DeleteCachedContext(ctx, cachedContextName); err != nil {
		logAndExit(1, "Failed to delete context: %s", err)
	}

	// success
	os.Exit(0)
}
