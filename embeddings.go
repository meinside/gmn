// embeddings.go
//
// Things for generating embeddings.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fatih/color"
	"google.golang.org/genai"

	gt "github.com/meinside/gemini-things-go"
)

// generate embeddings with given things
func doEmbeddingsGeneration(
	ctx context.Context,
	writer outputWriter,
	timeoutSeconds int,
	gtc *gt.Client,
	p params,
) (exit int, e error) {
	if p.Generation.Prompt == nil {
		return 1, fmt.Errorf("prompt is required for embeddings generation")
	}
	prompt := *p.Generation.Prompt
	taskType := p.Embeddings.EmbeddingsTaskType
	chunkSize := p.Embeddings.EmbeddingsChunkSize
	overlappedChunkSize := p.Embeddings.EmbeddingsOverlappedChunkSize
	vbs := p.Verbose

	writer.verbose(
		verboseMedium,
		vbs,
		"generating embeddings...",
	)

	if chunkSize == nil {
		chunkSize = new(defaultEmbeddingsChunkSize)
	}
	if overlappedChunkSize == nil {
		overlappedChunkSize = new(defaultEmbeddingsChunkOverlappedSize)
	}

	// chunk prompt text
	chunks, err := gt.ChunkText(prompt, gt.TextChunkOption{
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

	// embeddings task type
	var selectedTaskType gt.EmbeddingTaskType
	if taskType != nil {
		selectedTaskType = gt.EmbeddingTaskType(*taskType)
	}

	type embedding struct {
		Text    string    `json:"text"`
		Vectors []float32 `json:"vectors"`
	}
	type embeddings struct {
		Original string               `json:"original"`
		TaskType gt.EmbeddingTaskType `json:"taskType"`
		Chunks   []embedding          `json:"chunks"`
	}
	embeds := embeddings{
		Original: prompt,
		TaskType: selectedTaskType,
		Chunks:   []embedding{},
	}

	ctx, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	// iterate chunks and generate embeddings
	for i, text := range chunks.Chunks {
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
				i,
				err,
			)
		} else {
			embeds.Chunks = append(embeds.Chunks, embedding{
				Text:    text,
				Vectors: vectors[0],
			})
		}
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
