package media
import ("testing";"os";"fmt")
func TestDbgFate(t *testing.T){
 for _,f:=range []string{"al_sbr_cm_48_5.1","al_sbr_ps_04_new","al_sbr_ps_06_new"}{
  data,_:=os.ReadFile("/tmp/claude-0/-home-user-sweef/88880b6c-b9c0-5b8b-838a-110015e2aca6/scratchpad/fate/heaac/"+f+".mp4")
  SetFFmpegEnabled(false)
  _,err:=DecodeAudio(data)
  fmt.Println(f,err)
 }
}
