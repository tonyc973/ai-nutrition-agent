package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// userProfile is what the profile form holds; it pre-fills the form after login.
type userProfile struct {
	Sex          string   `json:"sex"`
	Age          float64  `json:"age"`
	Weight       float64  `json:"weight"`
	Height       float64  `json:"height"`
	Activity     string   `json:"activity"`
	Goal         string   `json:"goal"`
	Diet         string   `json:"diet"`
	Intolerances []string `json:"intolerances"`
	Notes        string   `json:"notes"`
}

func (p userProfile) forCalc() profile {
	return profile{p.Sex, p.Age, p.Weight, p.Height, p.Activity, p.Goal}
}

type user struct {
	Salt    string                `json:"salt"`
	Hash    string                `json:"hash"`
	Profile *userProfile          `json:"profile,omitempty"`
	Log     map[string][]logEntry `json:"log,omitempty"` // date "2026-10-20" -> what the user ate that day
}

// nutrientStat is one nutrient with this user's daily target; insights also fill in the measured amount.
type nutrientStat struct {
	nutrient
	Target  float64 `json:"target"`
	Amount  float64 `json:"amount"`  // insights: average per logged day
	Percent int     `json:"percent"` // insights: Amount as % of Target
}

// account is what the browser gets about the logged-in user: the profile plus the targets computed from it.
func account(username string, p *userProfile) map[string]any {
	out := map[string]any{"username": username, "profile": p}
	if p != nil {
		out["targets"], _ = calcTargets(p.forCalc()) // the profile was validated when it was saved
		var daily []nutrientStat
		for _, n := range nutrients {
			daily = append(daily, nutrientStat{nutrient: n, Target: dailyTarget(n.Key, *p)})
		}
		out["nutrients"] = daily
	}
	return out
}

var usersFile = "data/users.json"

// shortcut: all users live in one JSON file guarded by a mutex; move to a database for real traffic.
var (
	mu    sync.Mutex
	users = map[string]*user{} // username -> user
)

var usernameRE = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)

func loadUsers() error {
	b, err := os.ReadFile(usersFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &users)
}

// saveUsers writes a temp file and renames it, so a crash never leaves a half-written file. Caller holds mu.
func saveUsers() error {
	b, _ := json.MarshalIndent(users, "", "  ")
	if err := os.MkdirAll(filepath.Dir(usersFile), 0o700); err != nil {
		return err
	}
	tmp := usersFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, usersFile)
}

func hashPassword(password string, salt []byte) string {
	key, _ := pbkdf2.Key(sha256.New, password, salt, 600_000, 32) // OWASP's recommended iteration count for PBKDF2-SHA256
	return hex.EncodeToString(key)
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var c credentials
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return c, false
	}
	return c, true
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	c, ok := readCredentials(w, r)
	if !ok {
		return
	}
	if !usernameRE.MatchString(c.Username) {
		http.Error(w, "username must be 3-32 letters, digits, dots, dashes or underscores", http.StatusBadRequest)
		return
	}
	if len(c.Password) < 8 || len(c.Password) > 128 {
		http.Error(w, "password must be 8-128 characters", http.StatusBadRequest)
		return
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	u := &user{Salt: hex.EncodeToString(salt), Hash: hashPassword(c.Password, salt)} // hash outside the lock: it takes ~0.3s

	mu.Lock()
	_, taken := users[c.Username]
	var err error
	if !taken {
		users[c.Username] = u
		if err = saveUsers(); err != nil {
			delete(users, c.Username)
		}
	}
	mu.Unlock()
	if taken {
		http.Error(w, "username already taken", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "could not save the account", http.StatusInternalServerError)
		return
	}
	startSession(w, c.Username)
	writeJSON(w, account(c.Username, nil))
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	c, ok := readCredentials(w, r)
	if !ok {
		return
	}
	mu.Lock()
	u := users[c.Username]
	mu.Unlock()
	if u == nil || !passwordMatches(u, c.Password) {
		http.Error(w, "wrong username or password", http.StatusUnauthorized)
		return
	}
	startSession(w, c.Username)
	mu.Lock()
	p := u.Profile
	mu.Unlock()
	writeJSON(w, account(c.Username, p))
}

func passwordMatches(u *user, password string) bool {
	salt, err := hex.DecodeString(u.Salt)
	return err == nil && subtle.ConstantTimeCompare([]byte(hashPassword(password, salt)), []byte(u.Hash)) == 1
}

// handleLogout deletes the cookie. JWTs are stateless, so a copied token stays valid until it expires.
func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "token", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func handleMe(w http.ResponseWriter, r *http.Request, username string) {
	mu.Lock()
	p := users[username].Profile
	mu.Unlock()
	writeJSON(w, account(username, p))
}

func handleSaveProfile(w http.ResponseWriter, r *http.Request, username string) {
	var p userProfile
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || len(p.Notes) > 500 || len(p.Intolerances) > 20 || len(p.Diet) > 30 {
		http.Error(w, "invalid profile", http.StatusBadRequest)
		return
	}
	if _, err := calcTargets(p.forCalc()); err != nil { // same checks the calorie formula needs
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	mu.Lock()
	old := users[username].Profile
	users[username].Profile = &p
	err := saveUsers()
	if err != nil {
		users[username].Profile = old
	}
	mu.Unlock()
	if err != nil {
		http.Error(w, "could not save the profile", http.StatusInternalServerError)
		return
	}
	writeJSON(w, account(username, &p))
}

// startSession gives the browser a signed JWT in an HttpOnly cookie, which page JavaScript cannot read.
func startSession(w http.ResponseWriter, username string) {
	http.SetCookie(w, &http.Cookie{Name: "token", Value: signToken(username, time.Now()), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(tokenTTL.Seconds())})
}

// requireUser rejects requests without a valid JWT and passes the username on.
func requireUser(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := ""
		if c, err := r.Cookie("token"); err == nil {
			username, _ = verifyToken(c.Value, time.Now())
		}
		mu.Lock()
		_, exists := users[username] // a valid token for a deleted account is still rejected
		mu.Unlock()
		if !exists {
			http.Error(w, "please log in", http.StatusUnauthorized)
			return
		}
		next(w, r, username)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
