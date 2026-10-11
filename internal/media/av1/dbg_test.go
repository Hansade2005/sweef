package av1
import ("testing";"os")
func TestDbgLS(t *testing.T){
	W:=os.Getenv("W")
	data,_:=os.ReadFile(W+"/big2.tl.ivf")
	want,_:=os.ReadFile(W+"/big2.tl.yuv")
	pics:=decodeTileListFile(t,data,4)
	p:=pics[0]; b:=picBytes(p)
	for _,tl:=range []int{32,42,51} {
		tx,ty:=(tl%8)*64,(tl/8)*64
		n:=0
		for y:=0;y<64;y++{ for x:=0;x<64;x++{ i:=(ty+y)*p.Width+tx+x; if b[i]!=want[i]{ if n<12 {t.Logf("tile %d (%d,%d) ours %d ref %d",tl,x,y,b[i],want[i])}; n++ } } }
	}
}
