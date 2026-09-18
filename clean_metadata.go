package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
)

type FFprobeOutput struct {
	Streams []struct {
		Index int               `json:"index"`
		Tags  map[string]string `json:"tags"`
	} `json:"streams"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("Usage: %s <media_file>", os.Args[0])
	}

	inputFile := os.Args[1]

	// 1. Check if file exists and capture original permissions
	fileInfo, err := os.Stat(inputFile)
	if err != nil {
		return fmt.Errorf("error: File '%s' does not exist", inputFile)
	}
	originalMode := fileInfo.Mode()

	// Set up a context that cancels on SIGINT (Ctrl+C) for graceful cleanup
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// 2. Call ffprobe with strict parameters
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "quiet", "-output_format", "json", "-private", "-show_format", "-show_streams", "-show_programs", inputFile)
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running ffprobe: %w", err)
	}

	// 3. Parse JSON Output
	var probe FFprobeOutput
	if err := json.Unmarshal(outBuf.Bytes(), &probe); err != nil {
		return fmt.Errorf("error parsing ffprobe JSON: %w", err)
	}

	// 4. Extract language tags (case-insensitive)
	type langEntry struct {
		Index int
		Lang  string
	}
	var langs []langEntry

	for _, stream := range probe.Streams {
		for key, val := range stream.Tags {
			if strings.ToLower(key) == "language" {
				langs = append(langs, langEntry{Index: stream.Index, Lang: val})
				break // Found language for this stream
			}
		}
	}

	// 5. Build ffmpeg args stripping ALL metadata types
	ffmpegArgs := []string{
		"-nostdin",
		"-y",
		"-i", inputFile,
		"-map", "0",
		"-c", "copy",
		"-map_metadata", "-1",
		"-map_metadata:s", "-1",
		"-map_metadata:c", "-1",
		"-map_metadata:p", "-1",
	}

	for _, l := range langs {
		ffmpegArgs = append(ffmpegArgs, fmt.Sprintf("-metadata:s:%d", l.Index), fmt.Sprintf("language=%s", l.Lang))
	}

	// 6. Secure temp file in the same directory (for fast atomic rename)
	dir := filepath.Dir(inputFile)
	ext := filepath.Ext(inputFile)

	tmpFile, err := os.CreateTemp(dir, "cleanmeta-*"+ext)
	if err != nil {
		return fmt.Errorf("error creating temp file: %w", err)
	}
	tmpName := tmpFile.Name()
	tmpFile.Close() // Close so ffmpeg can safely overwrite it

	// Guaranteed cleanup on function return
	defer os.Remove(tmpName)

	fmt.Printf("Cleaning metadata for: %s\n", inputFile)

	// 7. Execute ffmpeg
	ffmpegArgs = append(ffmpegArgs, tmpName)
	ffCmd := exec.CommandContext(ctx, "ffmpeg", ffmpegArgs...)
	ffCmd.Stdout = os.Stdout
	ffCmd.Stderr = os.Stderr

	if err := ffCmd.Run(); err != nil {
		return fmt.Errorf("error running ffmpeg: %w", err)
	}

	// 8. Restore original file permissions to the temp file
	if err := os.Chmod(tmpName, originalMode); err != nil {
		return fmt.Errorf("error restoring original file permissions: %w", err)
	}

	// 9. Atomic rename
	if err := os.Rename(tmpName, inputFile); err != nil {
		return fmt.Errorf("error replacing original file with cleaned version: %w", err)
	}

	fmt.Printf("Successfully cleaned metadata for '%s'.\n", inputFile)
	return nil
}
