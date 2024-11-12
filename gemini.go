// gemini.go

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	gt "github.com/meinside/gemini-things-go"
)

// generate text with given things
func doGeneration(ctx context.Context, timeoutSeconds int, googleAIAPIKey, googleAIModel, systemInstruction, prompt string, promptFiles map[string][]byte, filepaths []*string, cachedContextName *string, vb []bool) (exit int, e error) {
	logVerbose(verboseMedium, vb, "generating...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIModel, googleAIAPIKey)
	if err != nil {
		return 1, err
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
	files := map[string]io.Reader{}
	filesToClose := []*os.File{}
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
			return 1, err
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
	type result struct {
		exit int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		if err := gtc.GenerateStreamed(
			ctx,
			prompt,
			files,
			func(data gt.StreamCallbackData) {
				if data.TextDelta != nil {
					fmt.Print(*data.TextDelta)
				} else if data.NumTokens != nil {
					// print the number of tokens
					logVerbose(
						verboseMinimum,
						vb,
						"input tokens: %d / output tokens: %d", data.NumTokens.Input, data.NumTokens.Output,
					)

					// success
					ch <- result{
						exit: 0,
						err:  nil,
					}
				} else if data.Error != nil {
					// error
					ch <- result{
						exit: 1,
						err:  fmt.Errorf("streaming failed: %s", gt.ErrToStr(data.Error)),
					}
				}
			},
			opts,
		); err != nil {
			// error
			ch <- result{
				exit: 1,
				err:  fmt.Errorf("generation failed: %s", gt.ErrToStr(err)),
			}
		}
	}()

	// wait for the generation to finish
	res := <-ch

	return res.exit, res.err
}

// cache context
func cacheContext(ctx context.Context, timeoutSeconds int, googleAIAPIKey, googleAIModel, systemInstruction string, prompt *string, promptFiles map[string][]byte, filepaths []*string, cachedContextDisplayName *string, vb []bool) (exit int, e error) {
	logVerbose(verboseMedium, vb, "caching context...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIModel, googleAIAPIKey)
	if err != nil {
		return 1, err
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
	files := map[string]io.Reader{}
	filesToClose := []*os.File{}
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
			return 1, err
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
		return 1, err
	}

	// success
	return 0, nil
}

// list cached contexts
func listCachedContexts(ctx context.Context, timeoutSeconds int, googleAIAPIKey, googleAIModel string, vb []bool) (exit int, e error) {
	logVerbose(verboseMedium, vb, "listing cached contexts...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIModel, googleAIAPIKey)
	if err != nil {
		return 1, err
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
			return 1, fmt.Errorf("no cached contexts.")
		}
	} else {
		return 1, err
	}

	// success
	return 0, nil
}

// delete cached context
func deleteCachedContext(ctx context.Context, timeoutSeconds int, googleAIAPIKey, googleAIModel string, cachedContextName string, vb []bool) (exit int, e error) {
	logVerbose(verboseMedium, vb, "deleting cached context...")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(googleAIModel, googleAIAPIKey)
	if err != nil {
		return 1, err
	}
	defer func() {
		if err := gtc.Close(); err != nil {
			logError("Failed to close client: %s", err)
		}
	}()

	if err := gtc.DeleteCachedContext(ctx, cachedContextName); err != nil {
		return 1, err
	}

	// success
	return 0, nil
}
