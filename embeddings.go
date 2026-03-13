// embeddings.go
//
// Things for generating embeddings.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fatih/color"
	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
)

// embeddings struct
type embeddings struct {
	Text string `json:"text,omitempty"` // only for text prompt

	Vectors []float32 `json:"vectors"`
}

// embeddingInfo struct
type embeddingInfo struct {
	Original string `json:"original"` // original prompt text or filepath

	TaskType gt.EmbeddingTaskType `json:"taskType"`
	Chunks   []embeddings         `json:"chunks"`
}

// generate embeddings with given things
func doEmbeddingsGeneration(
	ctx context.Context,
	writer outputWriter,
	timeoutSeconds int,
	gtc *gt.Client,
	p params,
) (exit int, e error) {
	prompts := []gt.Prompt{}

	// prompt text
	if p.Generation.Prompt != nil {
		prompts = append(prompts, gt.PromptFromText(*p.Generation.Prompt))
	}

	// files
	if len(p.Generation.Filepaths) > 0 {
		for _, fp := range p.Generation.Filepaths {
			if file, err := os.ReadFile(*fp); err == nil {
				prompts = append(prompts, gt.PromptFromBytesWithName(file, filepath.Base(*fp)))
			} else {
				return 1, fmt.Errorf("failed to read file for embeddings: %w", err)
			}
		}
	} else if p.Generation.Prompt == nil {
		return 1, fmt.Errorf("prompt or files required for embeddings generation")
	}

	taskType := p.Embeddings.EmbeddingsTaskType
	chunkSize := p.Embeddings.EmbeddingsChunkSize
	overlappedChunkSize := p.Embeddings.EmbeddingsOverlappedChunkSize
	vbs := p.Verbose

	// embeddings task type
	var selectedTaskType gt.EmbeddingTaskType
	if taskType != nil {
		selectedTaskType = gt.EmbeddingTaskType(*taskType)
	}

	if chunkSize == nil {
		chunkSize = new(defaultEmbeddingsChunkSize)
	}
	if overlappedChunkSize == nil {
		overlappedChunkSize = new(defaultEmbeddingsChunkOverlappedSize)
	}

	writer.verbose(
		verboseMedium,
		vbs,
		"generating embeddings...",
	)

	var embedInfo embeddingInfo
	embeds := []embeddingInfo{}

	// check all prompts
	for i, prompt := range prompts {
		switch prompt := prompt.(type) {
		case gt.TextPrompt:
			// chunk prompt text
			chunks, err := gt.ChunkText(prompt.Text, gt.TextChunkOption{
				ChunkSize:      *chunkSize,
				OverlappedSize: *overlappedChunkSize,
				EllipsesText:   "...",
			})
			if err != nil {
				return 1, fmt.Errorf(
					"failed to chunk text: %w",
					err,
				)
			}

			ctx, cancel := context.WithTimeout(
				ctx,
				time.Duration(timeoutSeconds)*time.Second,
			)
			defer cancel()

			embedInfo = embeddingInfo{
				Original: prompt.Text,
				TaskType: selectedTaskType,
			}

			// iterate chunks and generate embeddings
			for j, text := range chunks.Chunks {
				if vectors, err := gtc.GenerateEmbeddings(
					ctx,
					"",
					[]*genai.Content{
						genai.NewContentFromText(text, gt.RoleUser),
					},
					&selectedTaskType,
				); err != nil {
					return 1, fmt.Errorf(
						"embeddings failed for chunk[%d]: %w",
						j,
						err,
					)
				} else {
					embedInfo.Chunks = append(embedInfo.Chunks, embeddings{
						Text:    text,
						Vectors: vectors[0],
					})
				}
			}
		case gt.BytesPrompt:
			embedInfo = embeddingInfo{
				Original: prompt.Filename,
				TaskType: selectedTaskType,
			}

			if vectors, err := gtc.GenerateEmbeddings(
				ctx,
				"",
				[]*genai.Content{
					genai.NewContentFromBytes(prompt.Bytes, prompt.MIMEType, genai.RoleUser),
				},
				&selectedTaskType,
			); err != nil {
				return 1, fmt.Errorf(
					"embeddings failed for file[%d]: %w",
					i,
					err,
				)
			} else {
				embedInfo.Chunks = append(embedInfo.Chunks, embeddings{
					Vectors: vectors[0],
				})
			}
		default:
			return 1, fmt.Errorf(
				"unknown prompt type for embeddings: %T",
				prompt,
			)
		}

		embeds = append(embeds, embedInfo)
	}

	// print result in JSON format
	if encoded, err := json.Marshal(embeds); err != nil {
		return 1, fmt.Errorf(
			"embeddings encoding failed: %w",
			err,
		)
	} else {
		writer.printColored(
			color.FgHiWhite,
			"%s\n",
			string(encoded),
		)

		return 0, nil
	}
}
