package main

import (
	"fmt"
	"os"

	"github.com/jessevdk/go-flags"
)

// parameter definitions
type params struct {
	Prompt    string    `short:"p" long:"prompt" description:"Prompt to use" required:"true"`
	Filepaths []*string `short:"f" long:"filepath" description:"Path(s) of file(s)"`

	ConfigFilepath *string `short:"c" long:"config" description:"Config file's path (default: $XDG_CONFIG_HOME/gmn/config.json)"`

	GoogleAIAPIKey    *string `short:"k" long:"api-key" description:"API Key to use (can be ommitted if set in config)"`
	GoogleAIModel     *string `short:"m" long:"model" description:"Model to use (can be omitted)"`
	SystemInstruction *string `short:"s" long:"system" description:"System instruction (can be omitted)"`

	UserAgent *string `short:"u" long:"user-agent" description:"Override user-agent when fetching contents from URLs in the prompt"`

	Verbose []bool `short:"v" long:"verbose" description:"Show verbose logs"`
}

// main
func main() {
	var p params
	parser := flags.NewParser(&p, flags.HelpFlag|flags.PassDoubleDash)

	if _, err := parser.Parse(); err == nil {
		run(p)
	} else {
		if e, ok := err.(*flags.Error); ok {
			if e.Type == flags.ErrRequired || e.Type == flags.ErrHelp {
				parser.WriteHelp(os.Stdout)

				os.Exit(1)
			}
		}

		fmt.Printf("Failed to parse flags: %s\n", err)
	}
}
