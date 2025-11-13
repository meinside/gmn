// models.go
//
// Things for managing Gemini models.

package main

import (
	"context"
	"strings"
	"time"

	"github.com/fatih/color"

	gt "github.com/meinside/gemini-things-go"
)

// list models
func listModels(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey string,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"listing models...",
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

	if models, err := gtc.ListModels(ctx); err != nil {
		return 1, err
	} else {
		for _, model := range models {
			writer.printColored(
				color.FgHiGreen,
				"%s",
				model.Name,
			)
			writer.printColored(
				color.FgHiWhite,
				` (%s)`,
				model.DisplayName,
			)

			writer.printColored(
				color.FgWhite,
				`
  > input tokens: %d
  > output tokens: %d
  > supported actions: %s
`,
				model.InputTokenLimit,
				model.OutputTokenLimit,
				strings.Join(model.SupportedActions, ", "),
			)
		}
	}

	// success
	return 0, nil
}
