// cache.go
//
// Things for managing cached contexts.

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/fatih/color"

	gt "github.com/meinside/gemini-things-go"
)

// cache context
func cacheContext(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey, model string,
	systemInstruction string,
	prompts []gt.Prompt, promptFiles map[string][]byte, filepaths []*string,
	cachedContextDisplayName *string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"caching context...",
	)

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// gemini things client
	gtc, err := gt.NewClient(
		apiKey,
		gt.WithModel(model),
	)
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
	gtc.SetSystemInstructionFunc(func() string {
		return systemInstruction
	})

	// read & close files
	files, err := openFilesForPrompt(promptFiles, filepaths)
	if err != nil {
		return 1, err
	}
	defer func() {
		for _, toClose := range files {
			if err := toClose.Close(); err != nil {
				writer.error(
					"Failed to close file: %s",
					err,
				)
			}
		}
	}()

	// cache context and print the cached context's name
	for _, file := range files {
		prompts = append(prompts, gt.PromptFromFile(file.filename, file.reader))
	}
	if name, err := gtc.CacheContext(
		ctx,
		&systemInstruction,
		prompts,
		nil,
		nil,
		cachedContextDisplayName,
	); err == nil {
		writer.printColored(
			color.FgHiWhite,
			"%s",
			name,
		)
	} else {
		return 1, err
	}

	// success
	return 0, nil
}

// list cached contexts
func listCachedContexts(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"listing cached contexts...",
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

	if listed, err := gtc.ListAllCachedContexts(ctx); err == nil {
		if len(listed) > 0 {
			for _, content := range listed {
				writer.printColored(
					color.FgHiGreen,
					"%s",
					content.Name,
				)
				if len(content.DisplayName) > 0 {
					writer.printColored(
						color.FgHiWhite,
						" (%s)",
						content.DisplayName,
					)
				}
				writer.printColored(color.FgWhite, `
  > model: %s
  > created: %s
  > expires: %s
`,
					content.Model,
					content.CreateTime.Format("2006-01-02 15:04 MST"),
					content.ExpireTime.Format("2006-01-02 15:04 MST"),
				)
			}
		} else {
			return 1, fmt.Errorf("no cached contexts")
		}
	} else {
		return 1, err
	}

	// success
	return 0, nil
}

// delete cached context
func deleteCachedContext(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	cachedContextName string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"deleting cached context...",
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

	if err := gtc.DeleteCachedContext(ctx, cachedContextName); err != nil {
		return 1, err
	}

	// success
	return 0, nil
}
