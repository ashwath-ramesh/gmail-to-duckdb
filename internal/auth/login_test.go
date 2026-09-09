package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestDefaultOAuthPort(t *testing.T) {
	if DefaultOAuthPort != 41807 {
		t.Fatalf("got %d", DefaultOAuthPort)
	}
}

func TestLoginAuthURLStateAndPKCEUnique(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)

	auth1, out1 := startLogin(t, ctx, fake, nil)
	q1 := fake.hitAuth(t, auth1)
	code1 := fake.issue(q1.Get("code_challenge"))
	if getResp(t, callback(t, auth1, url.Values{"code": {code1}, "state": {q1.Get("state")}})).StatusCode != http.StatusOK {
		t.Fatal("first callback")
	}
	res1 := waitRes(t, ctx, out1)
	if res1.err != nil {
		t.Fatalf("first login: %v", res1.err)
	}

	auth2, out2 := startLogin(t, ctx, fake, nil)
	q2 := fake.hitAuth(t, auth2)
	code2 := fake.issue(q2.Get("code_challenge"))
	if getResp(t, callback(t, auth2, url.Values{"code": {code2}, "state": {q2.Get("state")}})).StatusCode != http.StatusOK {
		t.Fatal("second callback")
	}
	res2 := waitRes(t, ctx, out2)
	if res2.err != nil {
		t.Fatalf("second login: %v", res2.err)
	}

	if q1.Get("state") == "" || q2.Get("state") == "" {
		t.Fatal("missing state")
	}
	if q1.Get("state") == q2.Get("state") {
		t.Fatal("state reused")
	}
	if q1.Get("code_challenge_method") != "S256" || q2.Get("code_challenge_method") != "S256" {
		t.Fatal("auth request missing S256")
	}
	if q1.Get("code_challenge") == "" || q2.Get("code_challenge") == "" {
		t.Fatal("missing challenge")
	}
	if q1.Get("code_challenge") == q2.Get("code_challenge") {
		t.Fatal("challenge reused")
	}
	forms := fake.tokenForms()
	if len(forms) != 2 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q1, forms[0])
	assertExchangeMatchesAuth(t, q2, forms[1])
	if forms[0].Get("code_verifier") == forms[1].Get("code_verifier") {
		t.Fatal("verifier reused")
	}
	assertLoopbackRedirect(t, q1.Get("redirect_uri"))
	assertLoopbackRedirect(t, q2.Get("redirect_uri"))
}

func TestLoginBrowserCallback(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	authURL, out := startLogin(t, ctx, fake, nil)
	q := fake.hitAuth(t, authURL)
	if !strings.Contains(q.Get("scope"), "gmail.readonly") {
		t.Fatalf("scope %q", q.Get("scope"))
	}
	if q.Get("access_type") != "offline" {
		t.Fatalf("access_type %q", q.Get("access_type"))
	}
	code := fake.issue(q.Get("code_challenge"))
	resp := getResp(t, callback(t, authURL, url.Values{"code": {code}, "state": {q.Get("state")}}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback %d", resp.StatusCode)
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.tok.AccessToken == "" || res.tok.RefreshToken == "" {
		t.Fatalf("token %+v", res.tok)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
	assertLoopbackRedirect(t, q.Get("redirect_uri"))
	assertLoopbackRedirect(t, forms[0].Get("redirect_uri"))
}

func TestLoginPastedRedirectURL(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	authURL, out := startLogin(t, ctx, fake, pr)
	q := fake.hitAuth(t, authURL)
	code := fake.issue(q.Get("code_challenge"))
	if _, err := fmt.Fprintln(pw, callback(t, authURL, url.Values{"code": {code}, "state": {q.Get("state")}})); err != nil {
		t.Fatal(err)
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.tok.RefreshToken == "" {
		t.Fatal("missing refresh")
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
}

func TestLoginPastedBareCode(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	authURL, out := startLogin(t, ctx, fake, pr)
	q := fake.hitAuth(t, authURL)
	code := fake.issue(q.Get("code_challenge"))
	if _, err := fmt.Fprintln(pw, code); err != nil {
		t.Fatal(err)
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
	if forms[0].Get("code") != code {
		t.Fatalf("exchanged %q", forms[0].Get("code"))
	}
}

func TestLoginPastedPaddedBareCode(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	authURL, out := startLogin(t, ctx, fake, pr)
	q := fake.hitAuth(t, authURL)
	code := "opaque+/value=="
	fake.bind(code, q.Get("code_challenge"))
	if _, err := fmt.Fprintln(pw, code); err != nil {
		t.Fatal(err)
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
	if forms[0].Get("code") != code {
		t.Fatalf("exchanged %q", forms[0].Get("code"))
	}
}

func TestLoginPastedCodeValue(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	authURL, out := startLogin(t, ctx, fake, pr)
	q := fake.hitAuth(t, authURL)
	code := fake.issue(q.Get("code_challenge"))
	if _, err := fmt.Fprintln(pw, "code="+code); err != nil {
		t.Fatal(err)
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
}

func TestLoginPastedQueryWithMatchingState(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	authURL, out := startLogin(t, ctx, fake, pr)
	q := fake.hitAuth(t, authURL)
	code := fake.issue(q.Get("code_challenge"))
	if _, err := fmt.Fprintln(pw, "code="+code+"&state="+q.Get("state")); err != nil {
		t.Fatal(err)
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
}

func TestLoginRejectsMismatchedStateThenAcceptsGoodCallback(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	authURL, out := startLogin(t, ctx, fake, nil)
	q := fake.hitAuth(t, authURL)
	code := fake.issue(q.Get("code_challenge"))
	good := callback(t, authURL, url.Values{"code": {code}, "state": {q.Get("state")}})

	badState := callback(t, authURL, url.Values{"code": {code}, "state": {"other-state"}})
	if getResp(t, badState).StatusCode != http.StatusBadRequest {
		t.Fatal("wrong state")
	}
	if getResp(t, callback(t, authURL, url.Values{"state": {q.Get("state")}})).StatusCode != http.StatusBadRequest {
		t.Fatal("missing code")
	}
	if doCallback(t, http.MethodPost, good, "").StatusCode != http.StatusBadRequest {
		t.Fatal("POST")
	}
	wrongPath, err := url.Parse(good)
	if err != nil {
		t.Fatal(err)
	}
	wrongPath.Path = "/other"
	if getResp(t, wrongPath.String()).StatusCode != http.StatusBadRequest {
		t.Fatal("path")
	}
	if doCallback(t, http.MethodGet, good, "example.com").StatusCode != http.StatusBadRequest {
		t.Fatal("host")
	}
	stillRunning(t, out)

	if getResp(t, good).StatusCode != http.StatusOK {
		t.Fatal("good callback")
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
}

func TestLoginProviderErrorChecksState(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	authURL, out := startLogin(t, ctx, fake, nil)
	q := fake.hitAuth(t, authURL)
	bad := callback(t, authURL, url.Values{"error": {"access_denied"}, "state": {"nope"}})
	if getResp(t, bad).StatusCode != http.StatusBadRequest {
		t.Fatal("wrong-state error")
	}
	stillRunning(t, out)

	goodErr := callback(t, authURL, url.Values{"error": {"access_denied"}, "state": {q.Get("state")}})
	if getResp(t, goodErr).StatusCode != http.StatusBadRequest {
		t.Fatal("provider error status")
	}
	res := waitRes(t, ctx, out)
	if res.err == nil || !strings.Contains(res.err.Error(), "access_denied") {
		t.Fatalf("got %v", res.err)
	}
	if n := len(fake.tokenForms()); n != 0 {
		t.Fatalf("token requests %d", n)
	}
}

func TestLoginRejectsCodeBoundToOtherChallenge(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	authURL, out := startLogin(t, ctx, fake, nil)
	q := fake.hitAuth(t, authURL)
	foreign := fake.issue("foreign-challenge")
	resp := getResp(t, callback(t, authURL, url.Values{"code": {foreign}, "state": {q.Get("state")}}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback %d", resp.StatusCode)
	}
	res := waitRes(t, ctx, out)
	if res.err == nil {
		t.Fatal("expected exchange reject")
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	ver := forms[0].Get("code_verifier")
	if ver == "" {
		t.Fatal("exchange omitted verifier")
	}
	if oauth2.S256ChallengeFromVerifier(ver) != q.Get("code_challenge") {
		t.Fatal("exchange used a different attempt verifier")
	}
	if oauth2.S256ChallengeFromVerifier(ver) == "foreign-challenge" {
		t.Fatal("used foreign verifier")
	}
}

func TestLoginDuplicateCallbackAndPasteRace(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	authURL, out := startLogin(t, ctx, fake, pr)
	q := fake.hitAuth(t, authURL)
	code := fake.issue(q.Get("code_challenge"))
	cb := callback(t, authURL, url.Values{"code": {code}, "state": {q.Get("state")}})

	start := make(chan struct{})
	var wg sync.WaitGroup
	hit := func() {
		defer wg.Done()
		<-start
		resp, err := testHTTP.Get(cb)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}
	wg.Add(3)
	go hit()
	go hit()
	go func() {
		defer wg.Done()
		<-start
		_, _ = fmt.Fprintln(pw, code)
	}()
	close(start)

	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.tok.AccessToken == "" {
		t.Fatal("missing token")
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("duplicate completion blocked shutdown")
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
}

func TestLoginCancel(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, cancel := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	_, out := startLogin(t, ctx, fake, pr)
	cancel()
	res := waitRes(t, context.Background(), out)
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("got %v", res.err)
	}
}

func TestLoginStdinEOFStillAcceptsBrowser(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	authURL, out := startLogin(t, ctx, fake, strings.NewReader(""))
	q := fake.hitAuth(t, authURL)
	code := fake.issue(q.Get("code_challenge"))
	if getResp(t, callback(t, authURL, url.Values{"code": {code}, "state": {q.Get("state")}})).StatusCode != http.StatusOK {
		t.Fatal("callback")
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
}

func TestLoginPastedWrongAuthorityThenBrowserWorks(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	authURL, out := startLogin(t, ctx, fake, pr)
	q := fake.hitAuth(t, authURL)
	code := fake.issue(q.Get("code_challenge"))
	foreign := "http://127.0.0.1:1/?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(q.Get("state"))
	if _, err := fmt.Fprintln(pw, foreign); err != nil {
		t.Fatal(err)
	}
	stillRunning(t, out)
	if getResp(t, callback(t, authURL, url.Values{"code": {code}, "state": {q.Get("state")}})).StatusCode != http.StatusOK {
		t.Fatal("browser callback")
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
}

func TestLoginRejectsDuplicateAndMalformedBrowserQuery(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	authURL, out := startLogin(t, ctx, fake, nil)
	q := fake.hitAuth(t, authURL)
	st := url.QueryEscape(q.Get("state"))
	code := fake.issue(q.Get("code_challenge"))
	cd := url.QueryEscape(code)
	for _, raw := range []string{
		"state=" + st + "&state=other&code=" + cd,
		"state=" + st + "&code=" + cd + "&code=other",
		"state=" + st + "&error=denied&error=other",
		"state=" + st + "&code=" + cd + "&error=access_denied",
		"state=" + st + "&code=" + cd + "&x=%zz",
	} {
		if getResp(t, callbackRaw(t, authURL, raw)).StatusCode != http.StatusBadRequest {
			t.Fatalf("accepted %q", raw)
		}
	}
	stillRunning(t, out)
	if getResp(t, callback(t, authURL, url.Values{"code": {code}, "state": {q.Get("state")}})).StatusCode != http.StatusOK {
		t.Fatal("good callback")
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
}

func TestLoginPastedInvalidQueryThenAcceptsEncodedState(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	authURL, out := startLogin(t, ctx, fake, pr)
	q := fake.hitAuth(t, authURL)
	st := q.Get("state")
	code := fake.issue(q.Get("code_challenge"))
	redir := strings.TrimRight(authURL.Query().Get("redirect_uri"), "/")
	host := strings.TrimPrefix(redir, "http://")
	for _, line := range []string{
		"code=" + code + "&state=" + st + "&state=" + st,
		"code=" + code + "&state=" + st + "&error=access_denied",
		"http://user@" + host + "/?code=" + code + "&state=" + st,
		redir + "/?code=" + code + "&state=" + st + "#frag",
		"st%61te=wrong",
		"code=" + code + "&state=" + st + "&x=%zz",
	} {
		if _, err := fmt.Fprintln(pw, line); err != nil {
			t.Fatal(err)
		}
		stillRunning(t, out)
	}
	if _, err := fmt.Fprintln(pw, "st%61te="+url.QueryEscape(st)+"&code="+url.QueryEscape(code)); err != nil {
		t.Fatal(err)
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
	if forms[0].Get("code") != code {
		t.Fatalf("exchanged %q", forms[0].Get("code"))
	}
}

func TestLoginCancelClosesPartialHeader(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	t.Cleanup(cancel)
	authURL, out := startLogin(t, ctx, fake, nil)
	u, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Write([]byte("GET / HTTP/1.1\r\nHost: " + u.Host + "\r\n")); err != nil {
		t.Fatal(err)
	}
	cancel()
	wait, stop := context.WithTimeout(context.Background(), 4*time.Second)
	defer stop()
	res := waitRes(t, wait, out)
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("got %v", res.err)
	}
	_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 8)
	_, err = c.Read(buf)
	var ne net.Error
	if err == nil {
		t.Fatal("partial header connection still open")
	}
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("partial header connection stayed open")
	}
}

func TestLoginPastedMismatchedStateThenBrowserWorks(t *testing.T) {
	fake := newFakeOAuth(t)
	ctx, _ := loginCtx(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	authURL, out := startLogin(t, ctx, fake, pr)
	q := fake.hitAuth(t, authURL)
	code := fake.issue(q.Get("code_challenge"))
	if _, err := fmt.Fprintln(pw, "code="+code+"&state=wrong"); err != nil {
		t.Fatal(err)
	}
	stillRunning(t, out)
	if getResp(t, callback(t, authURL, url.Values{"code": {code}, "state": {q.Get("state")}})).StatusCode != http.StatusOK {
		t.Fatal("browser callback")
	}
	res := waitRes(t, ctx, out)
	if res.err != nil {
		t.Fatal(res.err)
	}
	forms := fake.tokenForms()
	if len(forms) != 1 {
		t.Fatalf("token requests %d", len(forms))
	}
	assertExchangeMatchesAuth(t, q, forms[0])
}

type loginRes struct {
	tok *oauth2.Token
	err error
}

type fakeOAuth struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	seq    int
	codes  map[string]string
	auths  []url.Values
	tokens []url.Values
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	t.Helper()
	f := &fakeOAuth{t: t, codes: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", f.serveAuth)
	mux.HandleFunc("/token", f.serveToken)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOAuth) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		Endpoint: oauth2.Endpoint{
			AuthURL:   f.srv.URL + "/auth",
			TokenURL:  f.srv.URL + "/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes: []string{"https://www.googleapis.com/auth/gmail.readonly"},
	}
}

func (f *fakeOAuth) serveAuth(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auths = append(f.auths, cloneValues(r.URL.Query()))
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeOAuth) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.tokens = append(f.tokens, cloneValues(r.PostForm))
	code := r.PostForm.Get("code")
	ver := r.PostForm.Get("code_verifier")
	want, ok := f.codes[code]
	f.mu.Unlock()
	if ver == "" {
		http.Error(w, `{"error":"missing_verifier"}`, http.StatusBadRequest)
		return
	}
	if !ok || oauth2.S256ChallengeFromVerifier(ver) != want {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  "access-" + code,
		"refresh_token": "refresh-" + code,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         "https://www.googleapis.com/auth/gmail.readonly",
	})
}

func (f *fakeOAuth) issue(challenge string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	code := fmt.Sprintf("synth-code-%d", f.seq)
	f.codes[code] = challenge
	return code
}

func (f *fakeOAuth) bind(code, challenge string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes[code] = challenge
}

func (f *fakeOAuth) hitAuth(t *testing.T, authURL *url.URL) url.Values {
	t.Helper()
	resp := getResp(t, authURL.String())
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("auth %d", resp.StatusCode)
	}
	qs := f.authQueries()
	if len(qs) == 0 {
		t.Fatal("provider missed auth request")
	}
	return qs[len(qs)-1]
}

func (f *fakeOAuth) authQueries() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.auths...)
}

func (f *fakeOAuth) tokenForms() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.tokens...)
}

func startLogin(t *testing.T, ctx context.Context, fake *fakeOAuth, in io.Reader) (*url.URL, <-chan loginRes) {
	t.Helper()
	if in == nil {
		in = strings.NewReader("")
	}
	opened := make(chan string, 1)
	out := make(chan loginRes, 1)
	go func() {
		tok, err := doLogin(ctx, fake.config(), 0, loginIO{
			in:  in,
			out: io.Discard,
			open: func(u string) error {
				select {
				case opened <- u:
				default:
				}
				return nil
			},
		})
		out <- loginRes{tok: tok, err: err}
	}()
	select {
	case raw := <-opened:
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u, out
	case res := <-out:
		t.Fatalf("login ended before auth url: %v", res.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	panic("unreachable")
}

func loginCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx, cancel
}

func waitRes(t *testing.T, ctx context.Context, out <-chan loginRes) loginRes {
	t.Helper()
	select {
	case res := <-out:
		return res
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	panic("unreachable")
}

func stillRunning(t *testing.T, out <-chan loginRes) {
	t.Helper()
	select {
	case res := <-out:
		t.Fatalf("login ended early: tok=%v err=%v", res.tok, res.err)
	case <-time.After(150 * time.Millisecond):
	}
}

func callback(t *testing.T, authURL *url.URL, q url.Values) string {
	t.Helper()
	return callbackRaw(t, authURL, q.Encode())
}

func callbackRaw(t *testing.T, authURL *url.URL, rawQuery string) string {
	t.Helper()
	u, err := url.Parse(authURL.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	assertLoopbackRedirect(t, u.String())
	return strings.TrimRight(u.String(), "?") + "?" + rawQuery
}

func assertLoopbackRedirect(t *testing.T, raw string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" {
		t.Fatalf("scheme %s", u.Scheme)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" || port == "" || port == "0" {
		t.Fatalf("redirect %s", raw)
	}
	if u.Path != "/" && u.Path != "" {
		t.Fatalf("path %s", u.Path)
	}
}

func assertExchangeMatchesAuth(t *testing.T, auth, token url.Values) {
	t.Helper()
	if auth.Get("code_challenge_method") != "S256" {
		t.Fatalf("challenge method %q", auth.Get("code_challenge_method"))
	}
	ch := auth.Get("code_challenge")
	ver := token.Get("code_verifier")
	if ch == "" || ver == "" {
		t.Fatal("missing PKCE fields")
	}
	if oauth2.S256ChallengeFromVerifier(ver) != ch {
		t.Fatal("verifier does not match S256 challenge")
	}
	if token.Get("code") == "" {
		t.Fatal("missing code")
	}
}

var testHTTP = &http.Client{Timeout: 2 * time.Second}

func getResp(t *testing.T, raw string) *http.Response {
	t.Helper()
	return doCallback(t, http.MethodGet, raw, "")
}

func doCallback(t *testing.T, method, raw, host string) *http.Response {
	t.Helper()
	var last error
	for i := 0; i < 40; i++ {
		req, err := http.NewRequest(method, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := testHTTP.Do(req)
		if err == nil {
			t.Cleanup(func() {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			})
			return resp
		}
		last = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s %s: %v", method, raw, last)
	return nil
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}
