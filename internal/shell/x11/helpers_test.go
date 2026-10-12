//go:build linux

package x11

import (
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
)

type shell0Event = shell.Event

const evResize = shell.EvResize

func shellCfg(w, h int) shell.Config { return shell.Config{Title: "t", Width: w, Height: h} }

func sleepMs(n int) { time.Sleep(time.Duration(n) * time.Millisecond) }
