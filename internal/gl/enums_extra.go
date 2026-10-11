package gl

import "fmt"

func init() {
	for i := 1; i < 32; i++ {
		Constants1[fmt.Sprintf("TEXTURE%d", i)] = uint32(0x84C0 + i)
	}
	Constants1["RGB8"] = GL_RGB8
	Constants1["RGBA8"] = GL_RGBA8
	delete(Constants1, "EXTENSIONS")
	delete(Constants1, "FIXED")
	delete(Constants1, "HALF_FLOAT")
	Constants2["HALF_FLOAT"] = GL_HALF_FLOAT
	delete(Constants2, "EXTENSIONS")
	delete(Constants2, "FIXED")
	for i := 1; i < 16; i++ {
		Constants2[fmt.Sprintf("DRAW_BUFFER%d", i)] = uint32(0x8825 + i)
	}
	Constants2["MAX_ELEMENT_INDEX"] = 0x8D6B
}
