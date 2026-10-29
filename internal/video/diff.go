package video

import (
	"image"
)

// Diff tuning: frames are reduced to a grid of grayscale cells (block means)
// so codec noise averages out; a cell counts as changed when its mean moves
// by more than cellDelta levels.
const (
	gridCols  = 160
	cellDelta = 12
)

// thumb is a downscaled grayscale picture.
type thumb struct {
	w, h   int // source size
	cols   int
	rows   int
	pixels []uint8
}

// newThumb averages img into a gridCols-wide grayscale grid that keeps the
// source aspect ratio.
func newThumb(img image.Image) *thumb {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	cols := min(gridCols, w)
	rows := max(1, h*cols/max(1, w))
	sum := make([]uint32, cols*rows)
	cnt := make([]uint32, cols*rows)
	luma := func(r, g, bl uint32) uint32 { return (299*r + 587*g + 114*bl) / 1000 }
	for y := 0; y < h; y++ {
		row := (y * rows / h) * cols
		switch m := img.(type) {
		case *image.RGBA:
			p := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				i := row + x*cols/w
				sum[i] += luma(uint32(p[4*x]), uint32(p[4*x+1]), uint32(p[4*x+2]))
				cnt[i]++
			}
		case *image.NRGBA:
			p := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				i := row + x*cols/w
				sum[i] += luma(uint32(p[4*x]), uint32(p[4*x+1]), uint32(p[4*x+2]))
				cnt[i]++
			}
		case *image.Gray:
			p := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				i := row + x*cols/w
				sum[i] += uint32(p[x])
				cnt[i]++
			}
		default:
			for x := 0; x < w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				i := row + x*cols/w
				sum[i] += luma(r>>8, g>>8, bl>>8)
				cnt[i]++
			}
		}
	}
	px := make([]uint8, len(sum))
	for i := range sum {
		if cnt[i] > 0 {
			px[i] = uint8(sum[i] / cnt[i])
		}
	}
	return &thumb{w: w, h: h, cols: cols, rows: rows, pixels: px}
}

// changedFraction is the share of grid cells that differ by more than
// cellDelta. A size change counts as fully changed.
func changedFraction(a, b *thumb) float64 {
	if a.w != b.w || a.h != b.h || len(a.pixels) != len(b.pixels) || len(a.pixels) == 0 {
		return 1
	}
	n := 0
	for i, p := range a.pixels {
		d := int(p) - int(b.pixels[i])
		if d > cellDelta || d < -cellDelta {
			n++
		}
	}
	return float64(n) / float64(len(a.pixels))
}
