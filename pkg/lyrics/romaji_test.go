package lyrics

import "testing"

// A line of kana is read into Latin letters, and a line without kana is
// left alone: Chinese and English lyrics gain nothing from a reading.
func TestRomaji(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"悴んだ心 ふるえる眼差し", "悴 nda 心 furueru 眼 差 shi"},
		{"きゃくほん", "kyakuhon"},
		{"しゃしん", "shashin"},
		{"がっこう", "gakkou"},
		{"コーヒー", "koohii"},
		{"我爱", ""},
		{"hello world", ""},
		// A song of Chinese lyrics gets no romaji on its lines, so nothing
		// shows where a Japanese song shows a reading.
		{"曾经的那些发生过的开心和难过", ""},
	} {
		if got := Romaji(c.text); got != c.want {
			t.Errorf("%q: %q, want %q", c.text, got, c.want)
		}
	}
}

// NetEase's hand-checked romaji goes to the line nearest its time, as
// translations do; a line without one is read from its own kana.
func TestBuildRomaji(t *testing.T) {
	lines := Build("[00:15.00]悴んだ心\n[00:20.00]世界で",
		"", "",
		"[00:15.00]kogonda kokoro")
	if len(lines) != 2 {
		t.Fatalf("lines %+v", lines)
	}
	if lines[0].Romaji != "kogonda kokoro" {
		t.Errorf("checked romaji %q", lines[0].Romaji)
	}
	// The second line has none from NetEase, so its kana is read.
	if lines[1].Romaji != "世 界 de" {
		t.Errorf("read romaji %q", lines[1].Romaji)
	}
}
