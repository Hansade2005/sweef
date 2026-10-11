package raster

import (
	"image/png"
	"os"
	"testing"
)

func TestSmokeDump(t *testing.T) {
	if os.Getenv("RASTER_DUMP") == "" {
		t.Skip()
	}
	c := NewContext(300, 100)
	c.SetFill(Paint{Color: Color{1, 1, 1, 1}})
	c.FillRect(0, 0, 300, 100)
	c.SetFill(Paint{Color: Black})
	c.SetFont("20px sans-serif")
	c.FillText("Hello, World fi", 10.3, 30.4, 0)
	c.SetFont("13px monospace")
	c.FillText("abc def", 10, 60, 0)
	m := c.MeasureText("Hello, World fi")
	t.Log(m)
	f, _ := os.Create(os.Getenv("RASTER_DUMP"))
	png.Encode(f, c.Surface().ToNRGBA())
	f.Close()
}
