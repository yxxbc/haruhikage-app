package catalog

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/yxxbc/haruhikage-app/internal/domain"
)

var errLyricFormat = errors.New("invalid lyrics format")
var (
	lineTiming  = regexp.MustCompile(`^\[(\d+),(\d+)\]`)
	qrcWords    = regexp.MustCompile(`\((\d+),(\d+)\)`)
	krcWords    = regexp.MustCompile(`<(\d+),(\d+),\d+>([^<]*)`)
	angleWords  = regexp.MustCompile(`<(-?\d+),(-?\d+)(?:,-?\d+)?>([^<]*)`)
	angleTag    = regexp.MustCompile(`<[-\d,]+>`)
	lrcStamp    = regexp.MustCompile(`^\[(\d+:\d+(?:[.:]\d+)?)\]`)
	languageTag = regexp.MustCompile(`\[language:([A-Za-z0-9+/=]+)\]`)
	qrcXML      = regexp.MustCompile(`(?s)LyricContent="(.*?)"\s*/>`)
)

type lyricWord struct {
	start, length int64
	text          string
}

func timedLyrics(lyrics domain.Lyrics) bool {
	for _, line := range strings.Split(strings.TrimPrefix(lyrics.LRC, "\ufeff"), "\n") {
		if lrcStamp.MatchString(strings.TrimSpace(line)) {
			return true
		}
	}
	for _, line := range strings.Split(lyrics.YRC, "\n") {
		if lineTiming.MatchString(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}
func lrcTime(ms int64) string { return fmt.Sprintf("[%02d:%02d.%03d]", ms/60000, ms/1000%60, ms%1000) }
func lrcMillis(s string) (int64, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, errLyricFormat
	}
	minutes, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || minutes > 100000 {
		return 0, errLyricFormat
	}
	secParts := strings.FieldsFunc(parts[1], func(r rune) bool { return r == '.' || r == ':' })
	if len(secParts) == 0 || len(secParts) > 2 {
		return 0, errLyricFormat
	}
	seconds, err := strconv.ParseInt(secParts[0], 10, 64)
	if err != nil || seconds > 59 {
		return 0, errLyricFormat
	}
	var ms int64
	if len(secParts) == 2 {
		fraction := secParts[1] + "000"
		ms, err = strconv.ParseInt(fraction[:3], 10, 64)
		if err != nil {
			return 0, errLyricFormat
		}
	}
	return minutes*60000 + seconds*1000 + ms, nil
}
func yrcText(at, length int64, words []lyricWord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%d,%d]", at, length)
	for _, w := range words {
		fmt.Fprintf(&b, "(%d,%d,0)%s", w.start, w.length, w.text)
	}
	return b.String()
}
func inflate(b []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, errLyricFormat
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxResponse+1))
	if err != nil || len(out) > maxResponse || !utf8.Valid(out) {
		return nil, errLyricFormat
	}
	return out, nil
}

// QRC is text followed by absolute (start,duration) word tags; MRC uses
// this same textual timing. XML attribute entities are decoded after extracting
// the attribute, not before (otherwise embedded quotes could truncate it).
func parseQRC(text string) (domain.Lyrics, error) {
	if strings.TrimSpace(text) == "" {
		return domain.Lyrics{}, nil
	}
	if strings.Contains(text, "LyricContent=") {
		m := qrcXML.FindStringSubmatch(text)
		if m == nil {
			return domain.Lyrics{}, errLyricFormat
		}
		text = m[1]
	}
	text = html.UnescapeString(strings.ReplaceAll(text, "\r", ""))
	var lrc, yrc []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		m := lineTiming.FindStringSubmatch(line)
		if m == nil {
			if lrcStamp.MatchString(line) || strings.HasPrefix(line, "[offset:") {
				lrc = append(lrc, line)
			}
			continue
		}
		at, e1 := strconv.ParseInt(m[1], 10, 64)
		length, e2 := strconv.ParseInt(m[2], 10, 64)
		if e1 != nil || e2 != nil || at > 7*24*3600*1000 || length > 7*24*3600*1000 {
			return domain.Lyrics{}, errLyricFormat
		}
		content := line[len(m[0]):]
		matches := qrcWords.FindAllStringSubmatchIndex(content, -1)
		var words []lyricWord
		var plain strings.Builder
		previous := 0
		for _, w := range matches {
			start, e1 := strconv.ParseInt(content[w[2]:w[3]], 10, 64)
			duration, e2 := strconv.ParseInt(content[w[4]:w[5]], 10, 64)
			if e1 != nil || e2 != nil || start > 7*24*3600*1000 || duration > 7*24*3600*1000 {
				return domain.Lyrics{}, errLyricFormat
			}
			word := content[previous:w[0]]
			words = append(words, lyricWord{start: start, length: duration, text: word})
			plain.WriteString(word)
			previous = w[1]
		}
		plain.WriteString(content[previous:])
		if len(words) > 0 {
			words[len(words)-1].text += content[previous:]
		}
		lrc = append(lrc, lrcTime(at)+plain.String())
		if len(words) > 0 {
			yrc = append(yrc, yrcText(at, length, words))
		}
	}
	if len(lrc) == 0 {
		return domain.Lyrics{}, errLyricFormat
	}
	return domain.Lyrics{LRC: strings.Join(lrc, "\n"), YRC: strings.Join(yrc, "\n")}, nil
}

// KRC: base64(krc1 + repeating-XOR(zlib(UTF-8))). Word offsets are relative
// to the line; language metadata contains line-indexed translations/romaji.
func decodeKRC(content string) (domain.Lyrics, error) {
	raw, err := base64.StdEncoding.DecodeString(content)
	if err != nil || len(raw) < 5 || string(raw[:4]) != "krc1" {
		return domain.Lyrics{}, errLyricFormat
	}
	key := []byte{0x40, 0x47, 0x61, 0x77, 0x5e, 0x32, 0x74, 0x47, 0x51, 0x36, 0x31, 0x2d, 0xce, 0xd2, 0x6e, 0x69}
	raw = raw[4:]
	for i := range raw {
		raw[i] ^= key[i%16]
	}
	decoded, err := inflate(raw)
	if err != nil {
		return domain.Lyrics{}, err
	}
	text := strings.ReplaceAll(string(decoded), "\r", "")
	var language struct {
		Content []struct {
			Type  int        `json:"type"`
			Lines [][]string `json:"lyricContent"`
		} `json:"content"`
	}
	if m := languageTag.FindStringSubmatch(text); m != nil {
		b, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil || json.Unmarshal(b, &language) != nil {
			return domain.Lyrics{}, errLyricFormat
		}
	}
	var lrc, yrc, translation, romaji []string
	index := 0
	for _, line := range strings.Split(text, "\n") {
		m := lineTiming.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		at, e1 := strconv.ParseInt(m[1], 10, 64)
		length, e2 := strconv.ParseInt(m[2], 10, 64)
		if e1 != nil || e2 != nil || at > 7*24*3600*1000 || length > 7*24*3600*1000 {
			return domain.Lyrics{}, errLyricFormat
		}
		content := line[len(m[0]):]
		var words []lyricWord
		var plain strings.Builder
		for _, w := range krcWords.FindAllStringSubmatch(content, -1) {
			offset, e1 := strconv.ParseInt(w[1], 10, 64)
			duration, e2 := strconv.ParseInt(w[2], 10, 64)
			if e1 != nil || e2 != nil || offset > 7*24*3600*1000 || duration > 7*24*3600*1000 {
				return domain.Lyrics{}, errLyricFormat
			}
			word := html.UnescapeString(w[3])
			plain.WriteString(word)
			words = append(words, lyricWord{start: at + offset, length: duration, text: word})
		}
		if len(words) == 0 {
			return domain.Lyrics{}, errLyricFormat
		}
		lrc = append(lrc, lrcTime(at)+plain.String())
		yrc = append(yrc, yrcText(at, length, words))
		for _, lang := range language.Content {
			if index >= len(lang.Lines) {
				continue
			}
			line := lrcTime(at) + html.UnescapeString(strings.Join(lang.Lines[index], ""))
			switch lang.Type {
			case 0:
				romaji = append(romaji, line)
			case 1:
				translation = append(translation, line)
			}
		}
		index++
	}
	if index == 0 {
		return domain.Lyrics{}, errLyricFormat
	}
	return domain.Lyrics{LRC: strings.Join(lrc, "\n"), YRC: strings.Join(yrc, "\n"), Translation: strings.Join(translation, "\n"), Romaji: strings.Join(romaji, "\n")}, nil
}

// MRC's client cipher is XXTEA-like arithmetic on signed 64-bit words,
// not standard 32-bit XXTEA. Go int64 intentionally wraps just as LX toLong.
func decodeMRC(text string) (string, error) {
	text = strings.TrimSpace(text)
	if len(text) < 32 || len(text)%16 != 0 {
		return "", errLyricFormat
	}
	data := make([]int64, len(text)/16)
	for i := range data {
		n, err := strconv.ParseUint(text[i*16:(i+1)*16], 16, 64)
		if err != nil {
			return "", errLyricFormat
		}
		data[i] = int64(n)
	}
	key := []int64{27303562373562475, 18014862372307051, 22799692160172081, 34058940340699235}
	const delta int64 = 2654435769
	y := data[0]
	for sum := int64(6+52/len(data)) * delta; sum != 0; sum -= delta {
		e := (sum >> 2) & 3
		for p := len(data) - 1; p >= 0; p-- {
			z := data[(p+len(data)-1)%len(data)]
			mx := ((z>>5 ^ y<<2) + (y>>3 ^ z<<4)) ^ ((y ^ sum) + (z ^ key[(int64(p)&3)^e]))
			data[p] -= mx
			y = data[p]
		}
	}
	units := make([]uint16, 0, len(data)*4)
	var b [8]byte
	for _, n := range data {
		binary.LittleEndian.PutUint64(b[:], uint64(n))
		for i := 0; i < 8; i += 2 {
			units = append(units, binary.LittleEndian.Uint16(b[i:i+2]))
		}
	}
	return strings.TrimRight(string(utf16.Decode(units)), "\x00"), nil
}

// See qrc_cipher.go for the nonstandard DES rounds used by the QQ client.
func decodeQRC(text string) (string, error) { return decodeQRCContext(context.Background(), text) }
func decodeQRCContext(ctx context.Context, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	encrypted, err := hex.DecodeString(text)
	if err != nil || len(encrypted) == 0 || len(encrypted)%8 != 0 {
		return "", errLyricFormat
	}
	key := []byte("!@#)(*$%123ZXC!@!@#)(NHL")
	schedules := [][16]uint64{qrcSchedule(key[16:24], true), qrcSchedule(key[8:16], false), qrcSchedule(key[:8], true)}
	for i := 0; i < len(encrypted); i += 8 {
		if i%2048 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		for _, schedule := range schedules {
			qrcBlock(encrypted[i:i+8], schedule)
		}
	}
	out, err := inflate(encrypted)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
