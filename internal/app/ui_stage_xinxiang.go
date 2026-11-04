package app

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yxxbc/haruhikage-app/pkg/lyrics"
)

// 心象 lays a line out as one wide band of light, each word rising from
// the band as it is sung and settling back into it: a word's glow spreads
// along the line before it lifts, so the band brightens ahead of the voice.
// Where 云阶 stacks a line in rows and 倾诉 breaks it large, 心象 keeps it
// on one line and lets the light do the work.
func (a *app) xinxiangLine(c *ui.Context, l *lyrics.Line, pos time.Duration, f, w, fade float32, accent, ink ui.Color) {
	if len(l.Words) == 0 {
		return
	}
	const lookahead = 80 * time.Millisecond
	// The band spans the line, its glow the part sung so far.
	lit := float32(0)
	for _, word := range l.Words {
		if pos >= word.Start {
			lit = clamp01(float32((pos - l.Words[0].Start).Seconds() / max(0.1, (l.Words[len(l.Words)-1].End-l.Words[0].Start).Seconds())))
		}
	}

	shapes := stageShapes(c, l, f)
	// Every word sits on the band: no scatter, no turn, only the lift of
	// the sung, and no swelling, so the hairline reads as one rule.
	for i := range shapes {
		shapes[i].x, shapes[i].y, shapes[i].rotate = 0, 0, 0
		shapes[i].scale = 1
	}

	ui.Column(c.Key(l.At)).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Opacity(fade).PassThrough().Children(func() {
		// The band runs the width of the view, behind the words.
		ui.Box(c.Key("band")).Absolute().Top(0).Left(0).Right(0).Bottom(0).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
			y := r.Y + r.H/2
			pad := r.W * 0.12
			p.Line(r.X+pad, y, r.X+r.W-pad, y, 1, white.Alpha(0.12))
			if lit <= 0 {
				return
			}
			head := r.X + pad + (r.W-2*pad)*lit
			p.Line(r.X+pad, y, head, y, 1.6, accent.Alpha(0.5))
			// The glow at the playhead, swelling with the line.
			for i := range 3 {
				t := float32(i+1) / 3
				reach := 40 * t
				p.Line(head-reach, y, head+reach, y, 0.6, accent.Alpha(0.18*(1-t)))
			}
		})
		ui.Row(c).AlignItems(ui.Center).Justify(ui.Center).Padding(0, 12).Children(func() {
			for i, word := range l.Words {
				s := shapes[i]
				since := float32((pos - (word.Start - lookahead)).Seconds())
				end := float32((pos - max(word.End, word.Start+150*time.Millisecond)).Seconds())
				// Lift while sung, settle after: a shallow arc, not a jump.
				lift := float32(0)
				if since > 0 {
					rise := clamp01(since / 0.35)
					lift = -f * 0.22 * easeOutCubic(rise)
				}
				if end > 0 {
					settle := clamp01(end / 0.5)
					lift *= 1 - easeOutCubic(settle)
				}
				s.y = lift
				a.stageWord(c.Key(i), word, s, pos, f, accent, ink)
			}
		})
	})
}

// 故障 tears a line apart as it comes in and as it leaves: slices of it
// shift sideways with a chromatic split, settling into place as the first
// word is due and breaking up again after the last. The slices move with
// the song's pulse, so a line lands like a signal locking on.
func (a *app) glitchLine(c *ui.Context, l *lyrics.Line, pos time.Duration, f, w, fade float32, accent, ink ui.Color) {
	if len(l.Words) == 0 {
		return
	}
	const lookahead = 80 * time.Millisecond
	first := l.Words[0].Start - lookahead
	last := max(l.Words[len(l.Words)-1].End, l.Words[len(l.Words)-1].Start+150*time.Millisecond)
	since := float32((pos - first).Seconds())
	after := float32((pos - last).Seconds())

	// How broken the line is: whole while it is sung, torn at its edges.
	broken := float32(0)
	switch {
	case since < 0:
		broken = 1
	case since < 0.3:
		broken = 1 - clamp01(since/0.3)
	case after > 0:
		broken = clamp01(after / 0.4)
	}

	shapes := stageShapes(c, l, f)
	// The line is torn sideways, not scattered: every word sits square.
	for i := range shapes {
		shapes[i].x, shapes[i].y, shapes[i].rotate = 0, 0, 0
		shapes[i].scale = 1
	}
	seed := float64(l.At/time.Millisecond) / 1000

	ui.Column(c.Key(l.At)).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Opacity(fade).PassThrough().Children(func() {
		ui.Row(c).AlignItems(ui.Center).Justify(ui.Center).Padding(0, 12).DrawOver(func(p *ui.Painter, r ui.Rect) {
			if broken <= 0.02 {
				return
			}
			// Slices: a few horizontal cuts, each shifted and split into
			// red and cyan, as a broken signal is.
			n := 5
			h := r.H
			for i := range n {
				fs := float64(i)
				top := r.Y + h*float32(i)/float32(n)
				sh := h / float32(n)
				// Each slice jumps at its own rate, changing as it goes.
				jump := float32(math.Sin(seed*3+fs*7+float64(pos/time.Millisecond)/90)) * 26 * broken
				alpha := 0.32 * broken * (0.5 + 0.5*float32(lineRandom(seed+fs, 3)))
				p.Fill(ui.Rect{X: r.X + jump - 3, Y: top, W: r.W, H: sh}, ui.RGB(255, 60, 90).Alpha(alpha*0.7), 0)
				p.Fill(ui.Rect{X: r.X - jump + 3, Y: top, W: r.W, H: sh}, ui.RGB(60, 220, 255).Alpha(alpha*0.7), 0)
			}
		}).Children(func() {
			for i, word := range l.Words {
				a.stageWord(c.Key(i), word, shapes[i], pos, f, accent, ink)
			}
		})
	})
}
