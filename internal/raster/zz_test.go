package raster
import ("testing";"fmt";"github.com/sweefbrowser/sweef/internal/font")
func TestZZ(t *testing.T){
 f:=font.Lookup("Liberation Sans",font.Style{Weight:400})
 m:=skiaGlyph(Glyph{R:'H',Face:f},16,16,0,false)
 fmt.Println(m.B)
 for y:=m.B.Y0;y<m.B.Y1;y++{ fmt.Println(y,m.Row(y)) }
 l:=lutForLum(0); fmt.Println(l[175],l[207],l[64],l[128],l[255]); w:=lutForLum(255); fmt.Println(w[64],w[128],w[200])
}
