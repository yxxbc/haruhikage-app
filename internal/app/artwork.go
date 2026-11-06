package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/egoist/mygo/ui"
	_ "golang.org/x/image/webp"
)

// artwork is a cover, decoded and ready to draw, with what the player
// derives from it: its color, and a blurred copy to fill a backdrop.
type artwork struct {
	bmp      *ui.Bitmap
	backdrop *ui.Bitmap
	tint     ui.Color // the cover's most vivid color
	failed   bool
	used     time.Time // when it was last asked for
	cost     int64     // encoded data, decoded pixels and mip/texture allowance
	release  time.Time // release CPU pixels on a later UI-thread access
}

// artCache owns its HTTP lifetime and a bounded worker queue. All cache
// mutation and Bitmap operations after publication run on the UI thread.
// Eviction drops references; MyGo owns texture retirement on the renderer.
// In particular, no timer or worker destroys a canvas/native GPU resource.
type artCache struct {
	art    map[string]*artwork
	busy   map[string]*artRequest
	jobs   chan *artRequest
	post   func(func())
	now    func() time.Time
	swept  time.Time
	asked  time.Time
	client *http.Client
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	budget int64
	bytes  int64
}

type artRequest struct {
	key, url string
	backdrop bool
	used     time.Time
	ctx      context.Context
	cancel   context.CancelFunc
}

const (
	artBudget    = 96 << 20
	artEntries   = 192
	artWorkers   = 4
	artPending   = 32
	artMaxBytes  = 12 << 20
	artMaxPixels = 4 << 20
)

// artKeep is how long a cover not asked for stays: the pages scrolled
// through over a session show thousands.
const artKeep = 5 * time.Minute

func newArtCache(post func(func())) *artCache {
	ctx, cancel := context.WithCancel(context.Background())
	c := &artCache{
		art: map[string]*artwork{}, busy: map[string]*artRequest{}, jobs: make(chan *artRequest, artPending),
		post: post, now: time.Now, swept: time.Now(), client: newArtClient(),
		ctx: ctx, cancel: cancel, budget: artBudget,
	}
	for range artWorkers {
		go c.worker()
	}
	return c
}

func newArtClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns: artWorkers, MaxIdleConnsPerHost: artWorkers,
		IdleConnTimeout: 30 * time.Second,
	}}
}

// Close is called on the UI thread during app shutdown. It cancels both
// queued and running requests; late Window.Update callbacks are discarded.
func (c *artCache) Close() {
	if c.closed {
		return
	}
	c.closed = true
	c.cancel()
	for _, req := range c.busy {
		req.cancel()
	}
	clear(c.busy)
	clear(c.art)
	c.bytes = 0
	c.client.CloseIdleConnections()
}

// sweep drops the covers not asked for in artKeep before the last one
// was, once a minute. The time the window draws nothing, idle or in the
// background, doesn't count: the first frame after it would drop every
// cover it shows, and they would flash as they load again.
func (c *artCache) sweep(now time.Time) {
	if now.Sub(c.swept) < time.Minute {
		return
	}
	c.swept = now
	for key, a := range c.art {
		if c.asked.Sub(a.used) > artKeep {
			c.drop(key)
		}
	}
	for key, req := range c.busy {
		if c.asked.Sub(req.used) > artKeep {
			req.cancel()
			delete(c.busy, key)
		}
	}
}

func (c *artCache) drop(key string) {
	c.bytes -= c.art[key].cost
	delete(c.art, key)
}

// trim applies both a decoded-memory LRU budget and a count bound (failed
// URLs must not accumulate forever either). The just-published entry is
// protected unless it alone exceeds the budget.
func (c *artCache) trim(keep string) {
	for len(c.art) > artEntries || c.bytes > c.budget {
		oldest := ""
		var used time.Time
		found := false
		for key, a := range c.art {
			if key == keep && len(c.art) > 1 {
				continue
			}
			if !found || a.used.Before(used) {
				oldest, used, found = key, a.used, true
			}
		}
		if !found {
			return
		}
		c.drop(oldest)
	}
}

// get preserves the provider's original URL, including signed query
// parameters. It never appends provider-specific resizing parameters.
func (c *artCache) get(url string, backdrop bool) *artwork {
	if url == "" || c.closed {
		return nil
	}
	key := url
	if backdrop {
		key += "#backdrop"
	}
	now := c.now()
	c.sweep(now)
	c.asked = now
	if a, ok := c.art[key]; ok {
		a.used = now
		if a.bmp != nil && !a.release.IsZero() && !now.Before(a.release) {
			// MyGo Bitmap.ReleasePixels must run on the drawing thread.
			a.bmp.ReleasePixels()
			a.release = time.Time{}
		}
		return a
	}
	if req := c.busy[key]; req != nil {
		req.used = now
		return nil
	}
	if len(c.busy) >= artPending {
		return nil // visible rows retry on the next completion/redraw
	}
	ctx, cancel := context.WithCancel(c.ctx)
	req := &artRequest{key: key, url: url, backdrop: backdrop, used: now, ctx: ctx, cancel: cancel}
	select {
	case c.jobs <- req:
		c.busy[key] = req
	default:
		cancel()
	}
	return nil
}

func (c *artCache) worker() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case req := <-c.jobs:
			if req.ctx.Err() != nil {
				continue
			}
			b, err := fetchArt(req.ctx, c.client, req.url)
			var bmp *ui.Bitmap
			var img image.Image
			var blurred *image.RGBA
			var tint ui.Color
			if err == nil && req.ctx.Err() == nil {
				bmp, img, err = decodeArt(b)
			}
			if err == nil && req.ctx.Err() == nil {
				tint = dominantColor(img)
				if req.backdrop {
					blurred = blurImage(img)
				}
			}
			if req.ctx.Err() != nil {
				continue
			}
			c.post(func() {
				if c.closed || req.ctx.Err() != nil || c.busy[req.key] != req {
					return
				}
				delete(c.busy, req.key)
				req.cancel()
				a := &artwork{failed: err != nil, used: c.now()}
				if err == nil {
					a.bmp, a.tint, a.release = bmp, tint, c.now().Add(artRelease)
					w, h := bmp.Size()
					a.cost = int64(w)*int64(h)*12 + int64(len(b))
					if blurred != nil {
						a.backdrop = ui.NewBitmap(blurred)
						a.cost += int64(len(blurred.Pix)) * 2
					}
				}
				c.art[req.key] = a
				c.bytes += a.cost
				c.trim(req.key)
			})
		}
	}
}

// artRelease is how long a cover keeps its pixels once loaded: past the
// frame that first shows it, and the smaller copies it shows from there.
const artRelease = 2 * time.Second

func fetchArt(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artwork: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > artMaxBytes {
		return nil, errors.New("artwork: encoded image exceeds budget")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, artMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > artMaxBytes {
		return nil, errors.New("artwork: encoded image exceeds budget")
	}
	return b, nil
}

// decodeArt decodes a cover twice: as a bitmap, which keeps the data to
// decode its pixels again after it drops them, and as an image.
func decodeArt(b []byte) (*ui.Bitmap, image.Image, error) {
	b = stillCover(b)
	if err := checkArtSize(b); err != nil {
		return nil, nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	bmp, err := ui.DecodeBitmap(b)
	return bmp, img, err
}

func fetchImage(url string) (image.Image, error) {
	client := newArtClient()
	defer client.CloseIdleConnections()
	b, err := fetchArt(context.Background(), client, url)
	if err != nil {
		return nil, err
	}
	return decodeImage(b)
}

func checkArtSize(b []byte) error {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > artMaxPixels {
		return errors.New("artwork: decoded image exceeds budget")
	}
	return nil
}

func decodeImage(b []byte) (image.Image, error) {
	b = stillCover(b)
	if err := checkArtSize(b); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}

// stillCover returns the data of a cover that the decoders read. Some
// covers are animated WebP, whatever their name says, which they cannot:
// those show their first frame.
func stillCover(b []byte) []byte {
	if still := firstWebPFrame(b); still != nil {
		return still
	}
	return b
}

// firstWebPFrame returns the first frame of an animated WebP as a still
// WebP of its own, or nil if b is not one.
func firstWebPFrame(b []byte) []byte {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return nil
	}
	chunk := func(b []byte) (name string, body, rest []byte, ok bool) {
		if len(b) < 8 {
			return "", nil, nil, false
		}
		n := int(binary.LittleEndian.Uint32(b[4:8]))
		if n > len(b)-8 {
			return "", nil, nil, false
		}
		end := min(len(b), 8+n+n&1)
		return string(b[:4]), b[8 : 8+n], b[end:], true
	}
	riff := func(chunks ...[]byte) []byte {
		body := []byte("WEBP")
		for _, c := range chunks {
			body = append(body, c...)
		}
		out := binary.LittleEndian.AppendUint32([]byte("RIFF"), uint32(len(body)))
		return append(out, body...)
	}
	for rest := b[12:]; ; {
		name, body, next, ok := chunk(rest)
		if !ok {
			return nil
		}
		rest = next
		if name != "ANMF" || len(body) < 16 {
			continue
		}
		// A frame: where it sits, its size, how long it shows, then its
		// image as the chunks of a still WebP, its alpha first if any.
		w, h := body[6:9], body[9:12]
		frame := body[16:]
		var alpha []byte
		for len(frame) > 0 {
			name, _, after, ok := chunk(frame)
			if !ok {
				return nil
			}
			whole := frame[:len(frame)-len(after)]
			switch name {
			case "ALPH":
				alpha = whole
			case "VP8L":
				return riff(whole)
			case "VP8 ":
				if alpha == nil {
					return riff(whole)
				}
				// Alpha needs the extended header, which carries the size.
				vp8x := []byte{'V', 'P', '8', 'X', 10, 0, 0, 0, 0x10, 0, 0, 0}
				vp8x = append(append(vp8x, w...), h...)
				return riff(vp8x, alpha, whole)
			}
			frame = after
		}
		return nil
	}
}

// shrink averages img down to n×n pixels.
func shrink(img image.Image, n int) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			x0, x1 := b.Min.X+x*b.Dx()/n, b.Min.X+(x+1)*b.Dx()/n
			y0, y1 := b.Min.Y+y*b.Dy()/n, b.Min.Y+(y+1)*b.Dy()/n
			var r, g, bl, cnt uint32
			for yy := y0; yy < max(y1, y0+1); yy += max(1, (y1-y0)/4) {
				for xx := x0; xx < max(x1, x0+1); xx += max(1, (x1-x0)/4) {
					cr, cg, cb, _ := img.At(xx, yy).RGBA()
					r, g, bl, cnt = r+cr>>8, g+cg>>8, bl+cb>>8, cnt+1
				}
			}
			out.SetRGBA(x, y, color.RGBA{uint8(r / cnt), uint8(g / cnt), uint8(bl / cnt), 255})
		}
	}
	return out
}

// dominantColor returns the most vivid color that covers much of img: the
// hue that its saturated pixels share most, at their mean.
func dominantColor(img image.Image) ui.Color {
	small := shrink(img, 24)
	const buckets = 18
	var weight [buckets]float64
	var sum [buckets][3]float64
	var gray [3]float64
	for i := 0; i < len(small.Pix); i += 4 {
		r, g, b := float64(small.Pix[i])/255, float64(small.Pix[i+1])/255, float64(small.Pix[i+2])/255
		h, s, v := hsv(r, g, b)
		gray[0], gray[1], gray[2] = gray[0]+r, gray[1]+g, gray[2]+b
		w := s * s * v
		if s < 0.15 || v < 0.15 {
			continue
		}
		k := int(h/360*buckets) % buckets
		weight[k] += w
		sum[k][0], sum[k][1], sum[k][2] = sum[k][0]+r*w, sum[k][1]+g*w, sum[k][2]+b*w
	}
	best := 0
	for k := range weight {
		if weight[k] > weight[best] {
			best = k
		}
	}
	if weight[best] < 2 {
		n := float64(len(small.Pix) / 4)
		return ui.RGB(uint8(gray[0]/n*255), uint8(gray[1]/n*255), uint8(gray[2]/n*255))
	}
	w := weight[best]
	return ui.RGB(uint8(sum[best][0]/w*255), uint8(sum[best][1]/w*255), uint8(sum[best][2]/w*255))
}

func hsv(r, g, b float64) (h, s, v float64) {
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	v = mx
	if mx == 0 {
		return 0, 0, 0
	}
	s = (mx - mn) / mx
	d := mx - mn
	switch {
	case d == 0:
		h = 0
	case mx == r:
		h = math.Mod((g-b)/d, 6) * 60
	case mx == g:
		h = ((b-r)/d + 2) * 60
	default:
		h = ((r-g)/d + 4) * 60
	}
	if h < 0 {
		h += 360
	}
	return h, s, v
}

// blurImage returns img shrunk and blurred, more saturated, to fill a
// backdrop behind what plays, scaled up as smooth as it is.
func blurImage(img image.Image) *image.RGBA {
	const n = 48
	small := shrink(img, n)
	px := make([][3]float64, n*n)
	for i := range px {
		r, g, b := float64(small.Pix[4*i]), float64(small.Pix[4*i+1]), float64(small.Pix[4*i+2])
		// More saturated, as light through colored glass.
		l := 0.3*r + 0.59*g + 0.11*b
		px[i] = [3]float64{l + (r-l)*1.5, l + (g-l)*1.5, l + (b-l)*1.5}
	}
	tmp := make([][3]float64, n*n)
	for range 3 {
		boxBlur(px, tmp, n, 5)
	}
	out := image.NewRGBA(image.Rect(0, 0, n, n))
	for i, p := range px {
		for c := range 3 {
			out.Pix[4*i+c] = uint8(math.Max(0, math.Min(255, p[c])))
		}
		out.Pix[4*i+3] = 255
	}
	return out
}

// boxBlur blurs px of n×n pixels by a box of radius r, across then down,
// clamping at the edges.
func boxBlur(px, tmp [][3]float64, n, r int) {
	at := func(i int) int { return max(0, min(n-1, i)) }
	for y := range n {
		for x := range n {
			var s [3]float64
			for k := -r; k <= r; k++ {
				p := px[y*n+at(x+k)]
				s[0], s[1], s[2] = s[0]+p[0], s[1]+p[1], s[2]+p[2]
			}
			d := float64(2*r + 1)
			tmp[y*n+x] = [3]float64{s[0] / d, s[1] / d, s[2] / d}
		}
	}
	for y := range n {
		for x := range n {
			var s [3]float64
			for k := -r; k <= r; k++ {
				p := tmp[at(y+k)*n+x]
				s[0], s[1], s[2] = s[0]+p[0], s[1]+p[1], s[2]+p[2]
			}
			d := float64(2*r + 1)
			px[y*n+x] = [3]float64{s[0] / d, s[1] / d, s[2] / d}
		}
	}
}
