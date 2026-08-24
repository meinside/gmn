// schemas.go
//
// Output schemas for the tools of the local MCP server.
//
// NOTE: Since protocol version 2026-07-28 (SEP-2106), `outputSchema` may use
// any JSON Schema 2020-12 keyword and `structuredContent` may be any JSON
// value; declaring output schemas lets clients know the shape of the
// structured results of these tools.

package main

import (
	"github.com/google/jsonschema-go/jsonschema"
)

// schema of `fileInfo`
func fileInfoSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"filename":     {Type: "string"},
			"absolutePath": {Type: "string"},
			"filesize":     {Type: "integer"},
			"mode":         {Type: "integer"},
			"modified":     {Type: "string", Format: "date-time"},
			"isDirectory":  {Type: "boolean"},
			"sys":          {Type: "string"},
		},
		Required: []string{
			"filename",
			"absolutePath",
			"filesize",
			"mode",
			"modified",
			"isDirectory",
		},
	}
}

// output schema of `gmn_list_models`
func listModelsOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"models": {
				Description: `Available Google AI models.`,
				Type:        "array",
				Items:       &jsonschema.Schema{Type: "object"},
			},
		},
		Required: []string{"models"},
	}
}

// output schema of `gmn_get_cwd`
func getCwdOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"currentWorkingDirectory": {
				Description: `An absolute path of the current working directory.`,
				Type:        "string",
			},
		},
		Required: []string{"currentWorkingDirectory"},
	}
}

// output schema of `gmn_stat_file`
func statFileOutputSchema() *jsonschema.Schema {
	return fileInfoSchema()
}

// output schema of `gmn_get_mimetype`
func getMimetypeOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"filepath":  {Type: "string"},
			"mimeType":  {Type: "string"},
			"extension": {Type: "string"},
		},
		Required: []string{"filepath", "mimeType", "extension"},
	}
}

// output schema of `gmn_list_files`
func listFilesOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"result": {
				Type: "array",
				Items: &jsonschema.Schema{
					Type: "object",
					Properties: map[string]*jsonschema.Schema{
						"filename":    {Type: "string"},
						"isDirectory": {Type: "boolean"},
						"mode":        {Type: "integer"},
						"info":        fileInfoSchema(),
					},
					Required: []string{"filename", "isDirectory", "mode"},
				},
			},
		},
		Required: []string{"result"},
	}
}

// output schema of `gmn_read_text_file`
func readTextFileOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"filepath": {Type: "string"},
			"content":  {Type: "string"},
		},
		Required: []string{"filepath", "content"},
	}
}

// output schema of `gmn_run_cmdline`
func runCmdlineOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"cmdline":  {Type: "string"},
			"exitCode": {Type: "integer"},
			"output": {
				Description: `The standard output of the executed commandline.`,
				Type:        "string",
			},
			"error": {
				Description: `The standard error of the executed commandline.`,
				Type:        "string",
			},
		},
		Required: []string{"cmdline", "exitCode"},
	}
}

// output schema of `gmn_do_http`
func doHTTPOutputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"requestedMethod": {Type: "string"},
			"requestedUrl":    {Type: "string"},
			"status":          {Type: "integer"},
			"headers": {
				Type: "object",
				AdditionalProperties: &jsonschema.Schema{
					Type:  "array",
					Items: &jsonschema.Schema{Type: "string"},
				},
			},
			"body": {Type: "string"},
		},
		Required: []string{"requestedMethod", "requestedUrl", "status", "body"},
	}
}
