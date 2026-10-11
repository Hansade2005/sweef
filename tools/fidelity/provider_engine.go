package main

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// engineProvider runs the full engine end to end: what a user gets from
// `sweef open` + element boxes + `sweef screenshot`.
type engineProvider struct{}

func (engineProvider) Name() string { return "engine" }

func (engineProvider) Run(url string, w, h int) ([]ElemBox, image.Image, error) {
	o := engine.DefaultOptions()
	o.ViewportWidth, o.ViewportHeight = w, h
	e := engine.New(o)
	defer e.Close()
	tab, err := e.NewTab("about:blank")
	if err != nil {
		return nil, nil, err
	}
	tab.SetViewport(w, h, 1, false, false)
	if err := tab.Navigate(url, nil); err != nil {
		return nil, nil, err
	}
	tab.Settle(300 * time.Millisecond)
	var boxes []ElemBox
	for _, r := range tab.ElementBoxes() {
		boxes = append(boxes, ElemBox{Tag: strings.ToLower(r.Tag), X: r.X, Y: r.Y, W: r.W, H: r.H, Has: r.Has})
	}
	img, err := png.Decode(bytes.NewReader(tab.PNGScreenshot(w, h, 1)))
	if err != nil {
		return boxes, nil, err
	}
	return boxes, img, nil
}

func init() { Register(engineProvider{}) }
