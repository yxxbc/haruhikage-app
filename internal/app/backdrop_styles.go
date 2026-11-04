package app

import (
	"math"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// The backdrops of Now Playing, as Folia's own backgrounds are
// (github.com/chthollyphile/folia-major): each paints the cover's colors
// behind the lyrics, and each moves to its own account of the music.
//
// A backdrop is described in one place, the list below: the settings that
// choose it, the cache that paints it and the tests that budget its frames
// all ask here, so a backdrop added below needs nothing else changed.

// A backdropStyle paints the backdrop of the song playing.
type backdropStyle struct {
	// id is what the settings keep; "" is the still one, which a settings
	// file written before there were backdrops means.
	id string
	// label is what the settings call it.
	label string
	// moves says the backdrop is painted again as the music plays: while
	// it does, the frame is asked for again, and it is painted on the GPU
	// alone, where one runs. A still backdrop is painted once a song.
	moves bool
	// draw paints the backdrop of a cover of tint into r: at seconds of
	// its own drift, the music's bass and its level, from 0 to 1.
	draw func(p *ui.Painter, r ui.Rect, art *artwork, tint ui.Color, drift, bass, level float32)
}

// backdropStyles are the backdrops: the still one first, which a settings
// file that names none means.
var backdropStyles = []backdropStyle{
	{
		id:    "",
		label: "静态",
		draw: func(p *ui.Painter, r ui.Rect, art *artwork, tint ui.Color, _, _, _ float32) {
			drawBackdrop(p, r, art, tint)
		},
	},
	{
		id:    "fog",
		label: "迷雾",
		moves: true,
		draw: func(p *ui.Painter, r ui.Rect, art *artwork, tint ui.Color, drift, bass, _ float32) {
			drawFog(p, r, tint, drift, bass)
		},
	},
	{
		id:    "latent",
		label: "隐现",
		moves: true,
		draw: func(p *ui.Painter, r ui.Rect, art *artwork, tint ui.Color, drift, bass, level float32) {
			drawLatent(p, r, tint, drift, bass, level)
		},
	},
}

// backdropStyleByID returns the backdrop with id, or the still one for an
// id no backdrop has: a settings file may name one this version dropped.
func backdropStyleByID(id string) backdropStyle {
	for _, b := range backdropStyles {
		if b.id == id {
			return b
		}
	}
	return backdropStyles[0]
}

// latentSource paints 隐现: a fluid of the cover's colors, warped into
// broad shapes with deep shade between them, and a dither over it that
// follows the same flow, so the grain bends with the fluid rather than
// sitting on it as a grid.
//
// Folia layers paper-design's MeshGradient and Dithering
// (@paper-design/shaders) with CSS; here both are one shader. The look is
// domain warping: a noise field bends the coordinates the dilute is read
// at, twice over, which is what makes the colors pool into shapes with
// edges instead of averaging into one flat wash.
const latentSource = `package main

var Time float
var Bass float
var Level float
var DitherTime float
var A vec4
var B vec4
var C vec4
var D vec4
var Back vec4

func hash(p vec2) float {
	return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453)
}

func noise(p vec2) float {
	i := floor(p)
	f := fract(p)
	u := f * f * (3.0 - 2.0*f)
	return mix(mix(hash(i), hash(i+vec2(1, 0)), u.x),
		mix(hash(i+vec2(0, 1)), hash(i+vec2(1, 1)), u.x), u.y)
}

func fbm(p vec2) float {
	v := 0.0
	amp := 0.5
	for i := 0; i < 4; i++ {
		v += amp * noise(p)
		p = p*2.07 + vec2(1.7, 9.2)
		amp *= 0.5
	}
	return v
}

// fluid is the field the colors are read through: a coarse, slowly turning
// swirl of noise, warped by a finer one, so its shapes have edges and
// never repeat as a grid.
func fluid(uv vec2, t float) float {
	p := uv * vec2(2.6, 1.9)
	q := vec2(fbm(p + vec2(0, t*0.06)), fbm(p + vec2(5.2, 1.3) + vec2(t*0.05, 0)))
	// A second warp bends the first, so the shapes curl into one another.
	r := vec2(fbm(p + 2.4*q + vec2(1.7, 9.2) + vec2(t*0.035, 0)),
		fbm(p + 2.4*q + vec2(8.3, 2.8) - vec2(0, t*0.03)))
	return fbm(p + 2.1*r)
}

// bayer4 is the 4x4 ordered-dither threshold at a cell, 0 to 1.
func bayer4(x int, y int) float {
	var m [16]float
	m[0] = 0.0
	m[1] = 8.0
	m[2] = 2.0
	m[3] = 10.0
	m[4] = 12.0
	m[5] = 4.0
	m[6] = 14.0
	m[7] = 6.0
	m[8] = 3.0
	m[9] = 11.0
	m[10] = 1.0
	m[11] = 9.0
	m[12] = 15.0
	m[13] = 7.0
	m[14] = 13.0
	m[15] = 5.0
	return m[(y%4)*4+(x%4)] / 16.0
}

func Fragment(dst vec4, pos vec2) vec4 {
	size := rectSize()
	uv := pos / size
	f := fluid(uv, Time)
	// The colors pool where the field is high and leave deep shade where
	// it is low, so the picture has shapes rather than an average. The
	// shade follows the whole field, not one band of it, or it would sit
	// in the same place however the fluid turned.
	c := mix(D.rgb, A.rgb, smoothstep(0.20, 0.66, f))
	c = mix(c, B.rgb, smoothstep(0.50, 0.90, f)*0.8)
	c = mix(c, C.rgb, smoothstep(0.26, 0.46, f)*smoothstep(0.62, 0.44, f)*0.5)
	c = mix(Back.rgb, c, smoothstep(0.04, 0.40, f)*0.9+0.10)

	// The dither, warped by the same field so the grain follows the flow:
	// cells slide and shear with it instead of forming a grid.
	bend := (f-0.5)*26.0
	cell := 2.5
	gx := int((pos.x+bend) / cell)
	gy := int((pos.y+bend*0.6) / cell)
	th := bayer4(gx, gy)
	dither := mix(Back.rgb, A.rgb, th)
	sl := c + (2.0*dither-1.0)*(sqrt(max(c, vec3(0.0)))-c)

	// At Folia's ditheringOpacity, over the whole field.
	out := mix(c, sl, 0.55+(0.10*Level))
	out *= 1.04 - 0.30*smoothstep(0.55, 1.0, uv.y)
	return vec4(out, 1)
}
`

var latentShader = sync.OnceValue(func() *shader.Shader { return shader.MustCompile(latentSource) })

// drawLatent paints 隐现 of tint in r at drift seconds, lit by bass and
// level, both from 0 to 1: the cover's four colors as a fluid mesh, with a
// fine dither of light over it.
func drawLatent(p *ui.Painter, r ui.Rect, tint ui.Color, drift, bass, level float32) {
	a, b, c, d, back := latentColors(tint)
	// The dither crawls at its own pace, a third of the mesh's: Folia's
	// ditheringSpeed 0.1 against its meshSpeed 0.3.
	latentShader().Draw(p, r, shader.Options{Uniforms: map[string]any{
		"Time": drift * 0.3, "DitherTime": drift * 0.1,
		"Bass": min(bass, 1), "Level": min(level, 1),
		"A": a, "B": b, "C": c, "D": d, "Back": back,
	}})
}

// latentColors returns the four colors 隐现 flows between for a cover of
// tint, and the shade behind them: its hue, spread around it, at depths
// that let white read over them. A gray cover stays gray, its colors a
// spread of its own lightness rather than of hues it does not have.
func latentColors(tint ui.Color) (a, b, c, d, back ui.Color) {
	h, s, v := hsv(float64(tint.R)/255, float64(tint.G)/255, float64(tint.B)/255)
	if s < 0.12 {
		// Gray: the fluid is shades of it. A gray cover would otherwise
		// paint a gray screen, so its darkest shades go far darker than the
		// cover itself, to give the shapes an edge to read against.
		return hsvColor(h, s, min(v*1.6, 0.98)), hsvColor(h, s, v*1.1),
			hsvColor(h, s, v*0.62), hsvColor(h, s, v*0.28), hsvColor(h, s, 0.06)
	}
	// Muted, as Folia's own palette is: a cover's hue well below full
	// saturation, so the fluid reads as paint rather than as neon.
	s = min(max(s*0.75, 0.34), 0.58)
	return hsvColor(h, s*0.85, 0.78), hsvColor(math.Mod(h+16, 360), s, 0.62),
		hsvColor(math.Mod(h-18, 360), s*0.9, 0.5), hsvColor(math.Mod(h+40, 360), s*0.7, 0.24),
		hsvColor(h, s*0.8, 0.07)
}

// backdropDesc describes the backdrop chosen, for its row in the settings:
// what it does, and what it costs.
func (a *app) backdropDesc() string {
	switch a.settings.Backdrop {
	case "fog":
		return "封面色调的烟雾缓缓升起，随低音起伏；会多用一些电量"
	case "latent":
		return "封面色彩化作流动的色块，叠一层细密的抖动光影，随低音变形；会多用一些电量"
	}
	return "封面色彩化作静止的渐变，几乎不耗电"
}
