package webaudio

import (
	"fmt"
	"testing"
)

func TestDbgMerger(t *testing.T) {
	c := NewContext(44100, true, 4, 256)
	m, _ := c.AddNode("channelmerger", map[string]any{"inputs": 4.0})
	kinds := []string{"sine", "square", "sawtooth", "triangle"}
	for i, k := range kinds {
		o, _ := c.AddNode("oscillator", nil)
		c.Set(o, "type", k)
		c.Connect(o, 0, m, i)
		c.Start(o, 0, 0, 0, false)
	}
	c.Connect(m, 0, c.Dest.ID, 0)
	c.Render(256)
	for ch := 0; ch < 4; ch++ {
		fmt.Println(ch, c.Output[ch][100:104])
	}
}
