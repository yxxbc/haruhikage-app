package app

import (
	"testing"
	"time"

	"github.com/yxxbc/haruhikage-app/internal/lyrics"
)

// Choosing a style from the stage's menu sets its lyrics in it, and
// choosing 流光 goes back to the classic.
func TestStageStyleMenu(t *testing.T) {
	a, tt, _ := playingApp()
	a.nowPlaying, a.npPane = true, paneStage
	a.pl.Seek(2 * time.Second)
	tt.Frame()
	for _, c := range []struct{ item, want string }{{"云阶", "partita"}, {"倾诉", "tilt"}, {"流光", ""}} {
		if err := tt.Click("律动效果"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem(c.item); err != nil {
			t.Fatalf("%v; the menu shows %q", err, tt.Menu())
		}
		tt.Frame()
		if a.settings.StageStyle != c.want {
			t.Errorf("chose %s: style %q, want %q", c.item, a.settings.StageStyle, c.want)
		}
	}
}

// 火花 in the stage's menu turns the sparks on, and again off, whatever
// the style.
func TestStageSparksMenu(t *testing.T) {
	a, tt, _ := playingApp()
	a.nowPlaying, a.npPane = true, paneStage
	a.settings.StageStyle = "tilt"
	tt.Frame()
	for _, want := range []bool{true, false} {
		if err := tt.Click("律动效果"); err != nil {
			t.Fatal(err)
		}
		if err := tt.ChooseMenuItem("火花"); err != nil {
			t.Fatalf("%v; the menu shows %q", err, tt.Menu())
		}
		tt.Frame()
		if a.settings.StageSparks != want || a.settings.StageStyle != "tilt" {
			t.Errorf("sparks %v in %q, want %v in tilt", a.settings.StageSparks, a.settings.StageStyle, want)
		}
	}
}

func words(texts ...string) []lyrics.Word {
	out := make([]lyrics.Word, len(texts))
	for i, s := range texts {
		out[i] = lyrics.Word{Text: s, Start: time.Duration(i) * time.Second, End: time.Duration(i+1) * time.Second}
	}
	return out
}

// A line breaks at its spaces and punctuation, punctuation staying with
// the word before it and a contraction whole, and a long run of
// characters in pieces.
func TestStageUnits(t *testing.T) {
	for _, c := range []struct {
		words []lyrics.Word
		want  [][2]int
	}{
		{words("从", "不 ", "主", "动 ", "示", "弱"), [][2]int{{0, 2}, {2, 4}, {4, 6}}},
		{words("Hello", ", ", "It", "’", "s ", "me"), [][2]int{{0, 2}, {2, 5}, {5, 6}}},
		{words("I ", "never ", "show"), [][2]int{{0, 1}, {1, 2}, {2, 3}}},
		{words("曾", "经", "的", "那", "些", "发", "生", "过"), [][2]int{{0, 4}, {4, 8}}},
	} {
		got := stageUnits(c.words)
		if len(got) != len(c.want) {
			t.Errorf("%v: units %v, want %v", c.words, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%v: units %v, want %v", c.words, got, c.want)
				break
			}
		}
	}
}

// 倾诉 splits a line into parts that follow one another, none empty, and
// rather after punctuation than nearest the middle.
func TestTiltSplitsAtPunctuation(t *testing.T) {
	ws := words("夜", "空", "中", "最", "亮", "的", "星，", "能", "否", "听", "清")
	units := stageUnits(ws)
	for n := 1; n <= min(4, len(units)); n++ {
		parts := splitParts(ws, units, n)
		if len(parts) != n || parts[0].from != 0 || parts[n-1].to != len(ws) {
			t.Fatalf("%d parts: %+v", n, parts)
		}
		for i := 1; i < n; i++ {
			if parts[i].from != parts[i-1].to || parts[i].from >= parts[i].to {
				t.Fatalf("%d parts: %+v", n, parts)
			}
		}
	}
	if parts := splitParts(ws, units, 2); parts[0].to != 7 {
		t.Errorf("split at %d, not after 星，", parts[0].to)
	}
}

// Every style the settings can name is one the stage has: a settings file
// naming a style this version does not know gets the classic, and one that
// does gets the style itself, so a style is added by its entry alone.
func TestStageStyleRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range stageStyles {
		if seen[s.id] {
			t.Errorf("%q is in the list twice", s.id)
		}
		seen[s.id] = true
		if s.label == "" {
			t.Errorf("%q has no label to show", s.id)
		}
		if s.draw == nil {
			t.Errorf("%q has nothing to draw it", s.id)
		}
		if got := stageStyleByID(s.id); got.id != s.id || got.label != s.label {
			t.Errorf("looking up %q: %q, %q", s.id, got.id, got.label)
		}
	}
	// The classic is the first, the one a settings file that names no style
	// means, so the button has a name to fall back to.
	if stageStyles[0].id != "" {
		t.Errorf("the first style is %q, not the classic", stageStyles[0].id)
	}
	if got := stageStyleByID("dropped-in-a-later-version"); got.id != "" {
		t.Errorf("a style this version does not have gives %q", got.id)
	}
}

// Every backdrop the settings can name is one the player has, and a name
// this version does not know is the still one: a settings file outlives
// the version that wrote it. The old Fog switch carries over to the
// backdrop it chose.
func TestBackdropStyles(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range backdropStyles {
		if seen[b.id] {
			t.Errorf("%q is in the list twice", b.id)
		}
		seen[b.id] = true
		if b.label == "" || b.draw == nil {
			t.Errorf("%q has no label or nothing to draw it", b.id)
		}
		if got := backdropStyleByID(b.id); got.id != b.id {
			t.Errorf("looking up %q gives %q", b.id, got.id)
		}
	}
	if backdropStyles[0].id != "" {
		t.Errorf("the first backdrop is %q, not the still one", backdropStyles[0].id)
	}
	if got := backdropStyleByID("dropped-later"); got.id != "" {
		t.Errorf("a backdrop this version does not have gives %q", got.id)
	}

	// A settings file of the Fog switch's time keeps the fog it chose, and
	// one that names a backdrop keeps that, whatever the switch says. A
	// file that names neither is one from before there was a choice, and
	// takes the backdrop the app starts with.
	for _, c := range []struct {
		fog, fogSet, backdropSet bool
		name, want               string
	}{
		{true, true, false, "", "fog"},       // the switch was on
		{false, true, false, "", "latent"},   // the switch was off, so the default
		{true, true, true, "latent", "latent"}, // a named backdrop wins
		{true, true, true, "partita", "partita"}, // a backdrop named wins
		{false, false, true, "fog", "fog"},   // named, switch never written
		{false, false, false, "", "latent"},  // neither: a file from before
	} {
		s := defaultSettings()
		s.Fog, s.fogSet, s.backdropSet = c.fog, c.fogSet, c.backdropSet
		if c.name != "" {
			s.Backdrop = c.name
		}
		s.migrateFog()
		if s.Backdrop != c.want {
			t.Errorf("fog=%v fogSet=%v backdropSet=%v name=%q: migrated to %q, want %q",
				c.fog, c.fogSet, c.backdropSet, c.name, s.Backdrop, c.want)
		}
	}
	// A new install takes 隐现 and the automatic curve.
	n := defaultSettings()
	if n.Backdrop != "latent" || !n.Equalizer || n.EQPreset != "auto" {
		t.Errorf("a new install: backdrop %q, equalizer %v, curve %q", n.Backdrop, n.Equalizer, n.EQPreset)
	}
}
