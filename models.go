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
	writer outputWriter,
	timeoutSeconds int,
	gtc *gt.Client,
	p params,
) (exit int, e error) {
	vbs := p.Verbose

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

	if models, err := gtc.ListModels(ctx); err != nil {
		return 1, err
	} else {
		for _, model := range models {
			// model name
			writer.printColored(
				color.FgHiGreen,
				"%s",
				model.Name,
			)

			// model display name
			if len(model.DisplayName) > 0 {
				writer.printColored(
					color.FgHiWhite,
					` (%s)`,
					model.DisplayName,
				)
			}

			// token limits and supported actions
			if model.InputTokenLimit > 0 && model.OutputTokenLimit > 0 && len(model.SupportedActions) > 0 {
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
			} else {
				writer.printColored(
					color.FgWhite,
					"\n",
				)
			}
		}
	}

	// success
	return 0, nil
}
