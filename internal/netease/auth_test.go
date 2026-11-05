package netease

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTripFunc is a transport that answers every request with fn.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Account reads the membership from the account as well as the profile,
// so that a Black Vinyl subscriber shows as one.
func TestAccountVIP(t *testing.T) {
	defer func(old *http.Client) { httpClient = old }(httpClient)
	httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"code":200,"account":{"vipType":11},"profile":{"userId":1,"nickname":"听歌的人","vipType":0}}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p, err := Account()
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != 1 || p.Nickname != "听歌的人" {
		t.Fatalf("account %+v", p)
	}
	if p.VIPType != 11 || !p.VIP() {
		t.Errorf("VIP type %d, VIP %v", p.VIPType, p.VIP())
	}
	var nobody *Profile
	if nobody.VIP() || (&Profile{}).VIP() {
		t.Error("an account without a membership is a VIP")
	}
}

// A redplus membership is SVIP: the account says only that it is paid,
// and its own detail says which of the paid kinds it is.
func TestAccountSVIP(t *testing.T) {
	defer func(old *http.Client) { httpClient = old }(httpClient)
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"code":200,"account":{"vipType":11},"profile":{"userId":1,"nickname":"听歌的人","vipType":11}}`
		if strings.Contains(r.URL.Path, "/user/detail/") {
			body = `{"code":200,"profile":{"userId":1,"vipRights":{"redplus":{"vipCode":300,"rights":true}}}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p, err := Account()
	if err != nil {
		t.Fatal(err)
	}
	if p.Membership() != MembershipSVIP || !p.VIP() {
		t.Fatalf("membership %v, VIP %v", p.Membership(), p.VIP())
	}
}
