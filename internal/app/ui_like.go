package app

import (
	"github.com/egoist/mygo/ui"
	"github.com/yxxbc/haruhikage-app/internal/domain"
)

func (a *app) toggleLike(t *domain.Track) {
	if t == nil || a.lib == nil {
		a.toast = "本地资料库不可用"
		return
	}
	track := *t
	liked := !a.liked[track.Key()]
	a.run(func() {
		err := a.lib.Favorite(a.ctx, track, liked)
		a.post(func() {
			if err != nil {
				a.toast = "喜欢状态保存失败"
				return
			}
			a.liked[track.Key()] = liked
			a.refreshLibrary()
			if a.route.page == pageFavorites {
				a.load(a.route)
			}
		})
	})
}
func (a *app) likeCurrent() { a.toggleLike(a.pl.Current()) }
func (a *app) likeButton(c *ui.Context, t *domain.Track, size float32, color ui.Color) {
	icon := iconHeart
	label := "喜欢"
	if a.liked[t.Key()] {
		icon = iconHeartFill
		color = a.pal.accent
		label = "取消喜欢"
	}
	if iconButton(c, icon, size, color, label).Clicked() {
		a.toggleLike(t)
	}
}
