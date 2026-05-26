package main

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"sync"
)

// ffprobeMissing caches the "ffprobe not on PATH" result so a bulk album doesn't
// fork-exec N times just to fail identically.
var ffprobeMissing sync.Once
var ffprobeAbsent bool

// probeVideo runs ffprobe to extract video width, height, and duration in seconds.
// Returns ok=false if ffprobe is missing or the file has no video stream.
func probeVideo(path string) (w, h int, durSec float64, ok bool) {
	ffprobeMissing.Do(func() {
		if _, err := exec.LookPath("ffprobe"); err != nil {
			ffprobeAbsent = true
		}
	})
	if ffprobeAbsent {
		return 0, 0, 0, false
	}

	type ffStream struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Duration  string `json:"duration"`
	}
	type ffFormat struct {
		Duration string `json:"duration"`
	}
	type ffOut struct {
		Streams []ffStream `json:"streams"`
		Format  ffFormat   `json:"format"`
	}

	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, false
	}
	var data ffOut
	if jerr := json.Unmarshal(out, &data); jerr != nil {
		return 0, 0, 0, false
	}
	var width, height int
	var durStr string
	for _, s := range data.Streams {
		if s.CodecType == "video" && s.Width > 0 && s.Height > 0 {
			width, height = s.Width, s.Height
			if s.Duration != "" {
				durStr = s.Duration
			}
			break
		}
	}
	if width == 0 || height == 0 {
		return 0, 0, 0, false
	}
	if durStr == "" {
		durStr = data.Format.Duration
	}
	dur, _ := strconv.ParseFloat(durStr, 64)
	return width, height, dur, true
}
