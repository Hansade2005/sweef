package font
import ("testing";"fmt")
func TestAdvTmp(t *testing.T){
 for _,c:=range []struct{f string;sz float64;s string}{{"Liberation Sans",16,"Hello world"},{"Liberation Sans",16,"i"},{"Liberation Sans",16,"iiiiiiiiii"},{"Liberation Sans",13,"iiiiiiiiii"},{"Liberation Sans",14.5,"Hello world"},{"Liberation Serif",16,"Hello world"},{"DejaVu Sans Mono",13,"Hello world"},{"Liberation Sans",16,"AVAVAV To"},{"Inter",16,"Hello world"}}{
  f:=Lookup(c.f,Style{400,false}); w:=0.0; wk:=0.0; var prev rune
  for _,r:=range c.s{ a:=f.Advance(r,c.sz); w+=a; if prev!=0{wk+=f.Kern(prev,r,c.sz)}; wk+=a; prev=r}
  m:=f.Metrics(c.sz)
  fmt.Println(c.f,c.sz,c.s,w,wk,m)
 }
}
