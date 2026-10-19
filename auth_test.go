package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Register, save a profile, log out, log back in and get the profile back; plus the ways it must fail.
func TestAuthFlow(t *testing.T) {
	usersFile = filepath.Join(t.TempDir(), "users.json")
	users = map[string]*user{}
	t.Setenv("JWT_SECRET", strings.Repeat("s", 32))
	srv := httptest.NewServer(routes())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	call := func(method, path, body string) int {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if got := call("POST", "/api/chat", `{"messages":[{"role":"user","content":"hi"}]}`); got != 401 {
		t.Fatalf("chat without login: %d", got)
	}
	if got := call("POST", "/api/register", `{"username":"ana","password":"short"}`); got != 400 {
		t.Fatalf("short password: %d", got)
	}
	if got := call("POST", "/api/register", `{"username":"ana","password":"secret123"}`); got != 200 {
		t.Fatalf("register: %d", got)
	}
	if got := call("POST", "/api/register", `{"username":"ana","password":"other1234"}`); got != 409 {
		t.Fatalf("duplicate register: %d", got)
	}
	if got := call("PUT", "/api/profile", `{"sex":"female","age":23,"weight":62,"height":168,"intolerances":["dairy"]}`); got != 200 {
		t.Fatalf("save profile: %d", got)
	}
	if got := call("POST", "/api/logout", ""); got != 204 {
		t.Fatalf("logout: %d", got)
	}
	if got := call("GET", "/api/me", ""); got != 401 {
		t.Fatalf("me after logout: %d", got)
	}
	if got := call("POST", "/api/login", `{"username":"ana","password":"wrong-pass"}`); got != 401 {
		t.Fatalf("wrong password: %d", got)
	}
	if got := call("POST", "/api/login", `{"username":"ana","password":"secret123"}`); got != 200 {
		t.Fatalf("login: %d", got)
	}

	// The profile survives a restart: reload the file into a fresh map.
	users = map[string]*user{}
	if err := loadUsers(); err != nil {
		t.Fatal(err)
	}
	p := users["ana"].Profile
	if p == nil || p.Weight != 62 || p.Intolerances[0] != "dairy" {
		t.Fatalf("profile not persisted: %+v", p)
	}
	if strings.Contains(users["ana"].Hash, "secret123") || len(users["ana"].Hash) != 64 {
		t.Fatal("password must be stored only as a hash")
	}
}

func TestJWT(t *testing.T) {
	t.Setenv("JWT_SECRET", strings.Repeat("k", 32))
	now := time.Now()
	token := signToken("ana", now)
	if got, err := verifyToken(token, now); err != nil || got != "ana" {
		t.Fatalf("valid token: %q %v", got, err)
	}
	header, rest, _ := strings.Cut(token, ".")
	_, sig, _ := strings.Cut(rest, ".")
	forged := header + "." + b64([]byte(`{"sub":"admin","exp":9999999999}`)) + "." + sig
	none := b64([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + b64([]byte(`{"sub":"ana","exp":9999999999}`)) + "."
	for name, tok := range map[string]string{"changed payload": forged, "alg none": none, "garbage": "a.b"} {
		if _, err := verifyToken(tok, now); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if _, err := verifyToken(token, now.Add(tokenTTL)); err == nil {
		t.Error("expired token must be rejected")
	}
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	if _, err := verifyToken(token, now); err == nil {
		t.Error("token signed with another secret must be rejected")
	}
}
