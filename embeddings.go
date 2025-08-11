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

// embeddings parameter constants
const (
	// https://ai.google.dev/gemini-api/docs/models/gemini#text-embedding
	defaultEmbeddingsChunkSize           uint = 2048 * 2
	defaultEmbeddingsChunkOverlappedSize uint = 64
)

// generate embeddings with given things
func doEmbeddingsGeneration(
	ctx context.Context,
	writer *outputWriter,
	timeoutSeconds int,
	apiKey, model string,
	prompt string,
	taskType *string,
	chunkSize, overlappedChunkSize *uint,
	vbs []bool,
) (exit int, e error) {
	writer.verbose(
		verboseMedium,
		vbs,
		"generating embeddings...",
	)

	if chunkSize == nil {
		chunkSize = ptr(defaultEmbeddingsChunkSize)
	}
	if overlappedChunkSize == nil {
		overlappedChunkSize = ptr(defaultEmbeddingsChunkOverlappedSize)
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
			writer.error("Failed to close client: %s", err)
		}
	}()

	// configure gemini things client
	gtc.SetTimeoutSeconds(timeoutSeconds)

	// embeddings task type
	var selectedTaskType gt.EmbeddingTaskType
	if taskType != nil {
		selectedTaskType = gt.EmbeddingTaskType(*taskType)
	}

	// iterate chunks and generate embeddings
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
