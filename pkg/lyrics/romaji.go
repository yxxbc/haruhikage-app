package lyrics

import "strings"

// Romaji gives a line of Japanese lyrics its Latin reading, so that someone
// who reads neither kanji nor kana can sing along: 夜に駆ける is "yoruni
// kakeru". Lines without kana get nothing from a reading—Chinese and
// English lyrics say what they say—so they return "".
//
// NetEase hand-checks a romaji for popular songs, which Build prefers; this
// is what a song without one falls back to. It reads the kana by table, and
// leaves a kanji as it is rather than guessing: 夜に駆ける is "夜ni駆keru",
// half a reading, which is still more use to a listener than none.
func Romaji(text string) string {
	if !hasKana(text) {
		return ""
	}
	return strings.Join(romajiWords(text), " ")
}

// hasKana reports whether s holds hiragana or katakana, which no other
// script the app shows uses.
func hasKana(s string) bool {
	for _, r := range s {
		if isKana(r) {
			return true
		}
	}
	return false
}

func isKana(r rune) bool {
	return (r >= 0x3041 && r <= 0x3096) || (r >= 0x30A1 && r <= 0x30FA)
}

// romajiWords splits a line into words of romaji. A run of kana becomes one
// word, and anything else—kanji, Latin, punctuation—goes on its own, so a
// reading never runs two words together.
func romajiWords(s string) []string {
	var out []string
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		if w := kanaToRomaji(string(run)); w != "" {
			out = append(out, w)
		} else {
			out = append(out, string(run))
		}
		run = run[:0]
	}
	for _, r := range s {
		switch {
		case isKana(r) || r == 'ー':
			run = append(run, r)
		case r == ' ' || r == '\u3000':
			flush()
		default:
			flush()
			out = append(out, string(r))
		}
	}
	flush()
	return out
}

// kanaToRomaji reads a run of hiragana, katakana or the long vowel mark.
// Katakana is folded to hiragana first, and a small っ doubles the consonant
// that follows it.
func kanaToRomaji(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := foldKana(rs[i])
		if r == 0 {
			// Not kana: the run had a mark this table does not read.
			return ""
		}
		// A small っ doubles the consonant of the syllable after it.
		if r == 'っ' {
			if next, ok := syllable(rs, i+1); ok && next != "" {
				b.WriteString(next[:1])
			}
			continue
		}
		// The long vowel mark holds the vowel of the kana before it.
		if r == 'ー' {
			if last := b.String(); last != "" {
				b.WriteString(last[len(last)-1:])
			}
			continue
		}
		// A syllable of two kana, as きゃ, first.
		if i+1 < len(rs) {
			if two, ok := pair[string([]rune{r, foldKana(rs[i+1])})]; ok {
				b.WriteString(two)
				i++
				continue
			}
		}
		if one, ok := single[r]; ok {
			b.WriteString(one)
			continue
		}
		return ""
	}
	return b.String()
}

// syllable returns the romaji of the kana at i, for the doubling of っ.
func syllable(rs []rune, i int) (string, bool) {
	if i >= len(rs) {
		return "", false
	}
	r := foldKana(rs[i])
	if one, ok := single[r]; ok {
		return one, true
	}
	if i+1 < len(rs) {
		if two, ok := pair[string([]rune{r, foldKana(rs[i+1])})]; ok {
			return two, true
		}
	}
	return "", false
}

// foldKana maps katakana to the same hiragana, and leaves hiragana as it
// is. It returns 0 for anything else.
func foldKana(r rune) rune {
	switch {
	case r == 'ー':
		return 'ー'
	case r >= 0x30A1 && r <= 0x30F6:
		// Katakana ァ..ヶ sit 0x60 above their hiragana.
		return r - 0x60
	case isKana(r):
		return r
	}
	return 0
}

// single reads one kana.
var single = map[rune]string{
	'あ': "a", 'い': "i", 'う': "u", 'え': "e", 'お': "o",
	'か': "ka", 'き': "ki", 'く': "ku", 'け': "ke", 'こ': "ko",
	'さ': "sa", 'し': "shi", 'す': "su", 'せ': "se", 'そ': "so",
	'た': "ta", 'ち': "chi", 'つ': "tsu", 'て': "te", 'と': "to",
	'な': "na", 'に': "ni", 'ぬ': "nu", 'ね': "ne", 'の': "no",
	'は': "ha", 'ひ': "hi", 'ふ': "fu", 'へ': "he", 'ほ': "ho",
	'ま': "ma", 'み': "mi", 'む': "mu", 'め': "me", 'も': "mo",
	'や': "ya", 'ゆ': "yu", 'よ': "yo",
	'ら': "ra", 'り': "ri", 'る': "ru", 'れ': "re", 'ろ': "ro",
	'わ': "wa", 'ゐ': "i", 'ゑ': "e", 'を': "o", 'ん': "n",
	'が': "ga", 'ぎ': "gi", 'ぐ': "gu", 'げ': "ge", 'ご': "go",
	'ざ': "za", 'じ': "ji", 'ず': "zu", 'ぜ': "ze", 'ぞ': "zo",
	'だ': "da", 'ぢ': "ji", 'づ': "zu", 'で': "de", 'ど': "do",
	'ば': "ba", 'び': "bi", 'ぶ': "bu", 'べ': "be", 'ぼ': "bo",
	'ぱ': "pa", 'ぴ': "pi", 'ぷ': "pu", 'ぺ': "pe", 'ぽ': "po",
	'ぁ': "a", 'ぃ': "i", 'ぅ': "u", 'ぇ': "e", 'ぉ': "o",
	'ゃ': "ya", 'ゅ': "yu", 'ょ': "yo", 'ゎ': "wa",
}

// pair reads the syllables of two kana, and the long vowels some take.
var pair = map[string]string{
	"きゃ": "kya", "きゅ": "kyu", "きょ": "kyo",
	"しゃ": "sha", "しゅ": "shu", "しょ": "sho",
	"ちゃ": "cha", "ちゅ": "chu", "ちょ": "cho",
	"にゃ": "nya", "にゅ": "nyu", "にょ": "nyo",
	"ひゃ": "hya", "ひゅ": "hyu", "ひょ": "hyo",
	"みゃ": "mya", "みゅ": "myu", "みょ": "myo",
	"りゃ": "rya", "りゅ": "ryu", "りょ": "ryo",
	"ぎゃ": "gya", "ぎゅ": "gyu", "ぎょ": "gyo",
	"じゃ": "ja", "じゅ": "ju", "じょ": "jo",
	"びゃ": "bya", "びゅ": "byu", "びょ": "byo",
	"ぴゃ": "pya", "ぴゅ": "pyu", "ぴょ": "pyo",
	"てゃ": "tha", "てゅ": "thu", "てょ": "tho",
	"でゃ": "dha", "でゅ": "dhu", "でょ": "dho",
	"ふぁ": "fa", "ふぃ": "fi", "ふぇ": "fe", "ふぉ": "fo",
	"うぁ": "wa", "うぃ": "wi", "うぇ": "we", "うぉ": "wo",
	"ゔ": "vu", "ゔぁ": "va", "ゔぃ": "vi", "ゔぇ": "ve", "ゔぉ": "vo",
	"いぇ": "ye", "きぇ": "kye", "しぇ": "she", "ちぇ": "che",
	"じぇ": "je", "にぇ": "nye", "ひぇ": "hye", "みぇ": "mye",
	"りぇ": "rye", "ぎぇ": "gye", "びぇ": "bye", "ぴぇ": "pye",
}
