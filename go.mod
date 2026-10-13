module github.com/sweefbrowser/sweef

go 1.26.0

require (
	github.com/dsnet/compress v0.0.1
	github.com/ebitengine/purego v0.11.1
	github.com/hajimehoshi/go-mp3 v0.3.4
	github.com/jfreymuth/oggvorbis v1.0.5
	github.com/thesyncim/gopus v0.1.2
	golang.org/x/image v0.47.0
	golang.org/x/net v0.17.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
	modernc.org/quickjs v0.25.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jfreymuth/vorbis v1.0.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/libquickjs v0.13.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace github.com/thesyncim/gopus => ./third_party/gopus

replace github.com/hajimehoshi/go-mp3 => ./third_party/go-mp3
