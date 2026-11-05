package netease

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

// The listener's session is NetEase's login cookie, MUSIC_U, which every
// request carries once signed in. The app keeps it in the system's
// keychain. Development builds keep it in a private file instead: each
// rebuild is signed anew, and the keychain would ask before every one of
// them read it.

const sessionSecret = "netease-session"

func sessionFile() (string, error) {
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session"), nil
}

func readSession() ([]byte, error) {
	if !mygo.IsDev() {
		return mygo.Secrets.Get(sessionSecret)
	}
	path, err := sessionFile()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func writeSession(b []byte) error {
	if !mygo.IsDev() {
		return mygo.Secrets.Set(sessionSecret, b)
	}
	path, err := sessionFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func deleteSession() {
	if !mygo.IsDev() {
		mygo.Secrets.Delete(sessionSecret)
		return
	}
	if path, err := sessionFile(); err == nil {
		os.Remove(path)
	}
}

var siteURL, _ = url.Parse(apiBase)

func init() {
	httpClient.Jar, _ = cookiejar.New(nil)
}

// RestoreSession puts the session from the keychain in the cookie jar, and
// reports whether there was one.
func RestoreSession() bool {
	b, err := readSession()
	if err != nil || len(b) == 0 {
		return false
	}
	var cookies []*http.Cookie
	for _, part := range strings.Split(string(b), ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			cookies = append(cookies, &http.Cookie{Name: name, Value: value, Path: "/"})
		}
	}
	httpClient.Jar.SetCookies(siteURL, cookies)
	return true
}

// SaveSession keeps the login cookies of the jar in the keychain.
func SaveSession() error {
	var parts []string
	for _, c := range httpClient.Jar.Cookies(siteURL) {
		if c.Name == "MUSIC_U" || c.Name == "__csrf" {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	if len(parts) == 0 {
		return errors.New("no session cookie")
	}
	return writeSession([]byte(strings.Join(parts, "; ")))
}

// ClearSession forgets the session, here and in the keychain.
func ClearSession() {
	httpClient.Jar.SetCookies(siteURL, []*http.Cookie{
		{Name: "MUSIC_U", Path: "/", MaxAge: -1},
		{Name: "__csrf", Path: "/", MaxAge: -1},
	})
	deleteSession()
}

// VIPRight is one of the memberships a profile may have.
type VIPRight struct {
	VIPCode int    `json:"vipCode"`
	Rights  bool   `json:"rights"`
	IconURL string `json:"iconUrl"`
}

// VIPRights are the paid memberships of a profile. NetEase names the
// highest one redplus, whose code 300 is SVIP; the associator is Black
// Vinyl VIP, and a music package is an album of songs without the rest.
type VIPRights struct {
	RedPlus      *VIPRight `json:"redplus"`
	Associator   *VIPRight `json:"associator"`
	MusicPackage *VIPRight `json:"musicPackage"`
	RedVipLevel  int       `json:"redVipLevel"`
}

// Membership is the best paid membership an account has.
type Membership uint8

const (
	MembershipNone Membership = iota
	MembershipMusicPackage
	MembershipVIP
	MembershipSVIP
)

type Profile struct {
	UserID    int64  `json:"userId"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatarUrl"`
	// VIPType is the account's membership: 0 none, 10 a music package,
	// 11 Black Vinyl VIP.
	VIPType   int       `json:"vipType"`
	VIPRights VIPRights `json:"vipRights"`
}

// Membership returns the best paid membership of the account.
func (p *Profile) Membership() Membership {
	if p == nil {
		return MembershipNone
	}
	r := &p.VIPRights
	if r.RedPlus != nil && r.RedPlus.Rights && r.RedPlus.VIPCode == 300 {
		return MembershipSVIP
	}
	if r.Associator != nil && r.Associator.Rights {
		return MembershipVIP
	}
	switch p.VIPType {
	case 10:
		return MembershipMusicPackage
	case 11:
		return MembershipVIP
	}
	if r.MusicPackage != nil && r.MusicPackage.Rights {
		return MembershipMusicPackage
	}
	if p.VIPType > 0 {
		return MembershipVIP
	}
	return MembershipNone
}

// VIP reports whether the account has a paid membership, of any kind.
func (p *Profile) VIP() bool { return p.Membership() != MembershipNone }

// Account returns the profile of who is signed in, or nil.
func Account() (*Profile, error) {
	var res struct {
		Account *struct {
			VIPType int `json:"vipType"`
		} `json:"account"`
		Profile *Profile `json:"profile"`
	}
	err := get("/api/nuser/account/get", nil, &res)
	if res.Profile != nil && res.Account != nil {
		res.Profile.VIPType = max(res.Profile.VIPType, res.Account.VIPType)
	}
	if err == nil && res.Profile != nil {
		res.Profile.fillVIPRights()
	}
	return res.Profile, err
}

// fillVIPRights reads the memberships of a paid account: the account
// endpoint only says that it is paid, while its own detail names SVIP.
func (p *Profile) fillVIPRights() {
	if p.VIPType == 0 || p.VIPRights.RedPlus != nil || p.VIPRights.Associator != nil {
		return
	}
	var res struct {
		Profile struct {
			VIPRights VIPRights `json:"vipRights"`
		} `json:"profile"`
	}
	if err := get("/api/v1/user/detail/"+strconv.FormatInt(p.UserID, 10), nil, &res); err == nil {
		p.VIPRights = res.Profile.VIPRights
	}
}

// QRKey starts a QR code login: the code shows LoginURL(key). The login
// goes through the eapi, as the desktop client's does.
func QRKey() (string, error) {
	var res struct {
		Code   int    `json:"code"`
		Unikey string `json:"unikey"`
	}
	if err := eapiPost("/api/login/qrcode/unikey", map[string]any{"type": 3}, &res); err != nil {
		return "", err
	}
	if res.Unikey == "" {
		return "", fmt.Errorf("netease: no QR key (code %d)", res.Code)
	}
	return res.Unikey, nil
}

func LoginURL(key string) string { return "https://music.163.com/login?codekey=" + key }

// QR login states, as NetEase reports them.
const (
	QRExpired   = 800
	QRWaiting   = 801
	QRScanned   = 802
	QRConfirmed = 803
)

// QRCheck reports where the login of key is: a QR state, or another code
// with NetEase's message when it refuses. Once scanned, it names who
// scanned; once confirmed, the session cookie is in the jar.
func QRCheck(key string) (code int, msg, nickname, avatar string, err error) {
	var r struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		Nickname  string `json:"nickname"`
		AvatarURL string `json:"avatarUrl"`
	}
	err = eapiPost("/api/login/qrcode/client/login", map[string]any{"key": key, "type": 3}, &r)
	return r.Code, r.Message, r.Nickname, r.AvatarURL, err
}

// Logout ends the session with NetEase too.
func Logout() {
	post("/api/logout", url.Values{}, &struct{}{})
	ClearSession()
}

// UserPlaylists returns the playlists a listener made, the liked songs
// first, and those they saved.
func UserPlaylists(uid int64) (made, saved []Playlist, err error) {
	var res struct {
		Playlist []struct {
			Playlist
			Subscribed bool `json:"subscribed"`
		} `json:"playlist"`
	}
	q := url.Values{"uid": {strconv.FormatInt(uid, 10)}, "limit": {"200"}, "offset": {"0"}}
	if err := get("/api/user/playlist", q, &res); err != nil {
		return nil, nil, err
	}
	for _, p := range res.Playlist {
		if p.Subscribed {
			saved = append(saved, p.Playlist)
		} else {
			made = append(made, p.Playlist)
		}
	}
	return made, saved, nil
}

// RecommendedPlaylists returns the day's playlists picked for the listener.
func RecommendedPlaylists() ([]Playlist, error) {
	var res struct {
		Recommend []struct {
			Playlist
			Playcount float64 `json:"playcount"`
		} `json:"recommend"`
	}
	if err := get("/api/v1/discovery/recommend/resource", nil, &res); err != nil {
		return nil, err
	}
	out := make([]Playlist, len(res.Recommend))
	for i, r := range res.Recommend {
		out[i] = r.Playlist
		out[i].PlayCount = r.Playcount
	}
	return out, nil
}

// LikedIDs returns the songs a listener likes, in no order.
func LikedIDs(uid int64) ([]int64, error) {
	var res struct {
		Code int     `json:"code"`
		IDs  []int64 `json:"ids"`
	}
	if err := eapiPost("/api/song/like/get", map[string]any{"uid": strconv.FormatInt(uid, 10)}, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("netease: liked songs: code %d", res.Code)
	}
	return res.IDs, nil
}

// Play is a song the listener played, on one of their devices.
type Play struct {
	Track Track
	At    time.Time
	// Device is the system NetEase names the device by: iphone, android,
	// osx, pc, web…
	Device string
}

// RecentPlays returns the songs the listener played last, on any device
// signed in as them, the latest first.
func RecentPlays(limit int) ([]Play, error) {
	var res struct {
		Code int `json:"code"`
		Data struct {
			List []struct {
				Data     Track `json:"data"`
				PlayTime int64 `json:"playTime"` // milliseconds
				Terminal struct {
					OS string `json:"os"`
				} `json:"multiTerminalInfo"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := eapiPost("/api/play-record/song/list", map[string]any{"limit": limit}, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("netease: recent plays: code %d", res.Code)
	}
	plays := make([]Play, len(res.Data.List))
	songs := make([]Track, len(plays))
	for i, l := range res.Data.List {
		songs[i] = l.Data
		plays[i] = Play{At: time.UnixMilli(l.PlayTime), Device: l.Terminal.OS}
	}
	// They come without privileges, as the daily songs do.
	detailPrivileges(songs)
	for i := range plays {
		plays[i].Track = songs[i]
	}
	return plays, nil
}

// Like likes a song, adding it to the listener's liked songs, or unlikes it.
func Like(id int64, like bool) error {
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	data := map[string]any{"alg": "itembased", "trackId": strconv.FormatInt(id, 10), "like": strconv.FormatBool(like), "time": "3"}
	if err := eapiPost("/api/radio/like", data, &res); err != nil {
		return err
	}
	if res.Code != 200 {
		return fmt.Errorf("netease: like: code %d %s", res.Code, res.Msg)
	}
	return nil
}
