package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// downloadProgress redraws one line while a download runs, so a large file does not look like a hang.
// It draws nothing when stdout is not a terminal: a service log would collect one line per update.
type downloadProgress struct {
	live  bool
	drawn bool
	width int
	last  time.Time
}

func newDownloadProgress() *downloadProgress {
	return &downloadProgress{live: term.IsTerminal(int(os.Stdout.Fd()))}
}

// report redraws the line, at most ten times a second so a fast download does not flood the terminal.
func (p *downloadProgress) report(complete, total int64) {
	if !p.live {
		return
	}
	now := time.Now()
	finished := total > 0 && complete >= total
	if !finished && now.Sub(p.last) < 100*time.Millisecond {
		return
	}
	p.last = now
	p.drawn = true
	line := progressLine(complete, total)
	// Padded to the widest line so far, so a shorter one cannot leave the tail of the last one behind.
	fmt.Printf("\r%-*s", p.width, line)
	if len(line) > p.width {
		p.width = len(line)
	}
}

// finish ends the line report has been redrawing.
func (p *downloadProgress) finish() {
	if p.drawn {
		fmt.Println()
	}
}

const progressBarWidth = 20

// progressLine is the bar, the percentage and the sizes. Without a total there is no percentage to
// show, so it falls back to what has arrived.
func progressLine(complete, total int64) string {
	if total <= 0 {
		return formatBytes(complete)
	}
	percent := int(complete * 100 / total)
	filled := percent * progressBarWidth / 100
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", progressBarWidth-filled)
	return fmt.Sprintf("[%s] %3d%%  %s/%s", bar, percent, formatBytes(complete), formatBytes(total))
}

// formatBytes is a size a person can read.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, exp := float64(n)/unit, 0
	for value >= unit && exp < 3 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %siB", value, []string{"K", "M", "G", "T"}[exp])
}
