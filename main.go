package main

import (
	"bufio"
	"cmp"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
)

//go:embed static
var static embed.FS

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func main() {
	loadDotEnv(".env")
	for _, k := range []string{"OPENAI_API_KEY", "SPOONACULAR_API_KEY", "JWT_SECRET"} {
		if os.Getenv(k) == "" {
			log.Fatalf("missing %s (put it in the .env file)", k)
		}
	}
	if len(os.Getenv("JWT_SECRET")) < 32 {
		log.Fatal("JWT_SECRET must be at least 32 characters (generate one with: openssl rand -hex 32)")
	}
	if os.Getenv("USDA_API_KEY") == "" {
		log.Println("warning: USDA_API_KEY is not set, using DEMO_KEY (a few food lookups per hour); get a free key at https://fdc.nal.usda.gov/api-key-signup")
	}
	if err := loadUsers(); err != nil {
		log.Fatalf("reading %s: %v", usersFile, err)
	}
	port := cmp.Or(os.Getenv("PORT"), "8090")
	log.Println("App running at http://localhost:" + port)
	log.Fatal(http.ListenAndServe(":"+port, routes()))
}

func routes() *http.ServeMux {
	site, _ := fs.Sub(static, "static")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(site))
	mux.HandleFunc("POST /api/register", handleRegister)
	mux.HandleFunc("POST /api/login", handleLogin)
	mux.HandleFunc("POST /api/logout", handleLogout)
	mux.HandleFunc("GET /api/me", requireUser(handleMe))
	mux.HandleFunc("PUT /api/profile", requireUser(handleSaveProfile))
	mux.HandleFunc("POST /api/chat", requireUser(handleChat)) // logged-in only, so strangers can't spend the API keys
	mux.HandleFunc("GET /api/log", requireUser(handleGetLog))
	mux.HandleFunc("POST /api/log", requireUser(handleAddLog))
	mux.HandleFunc("DELETE /api/log/{date}/{id}", requireUser(handleDeleteLog))
	mux.HandleFunc("GET /api/insights", requireUser(handleInsights))
	mux.HandleFunc("GET /api/insights/advice", requireUser(handleAdvice))
	mux.HandleFunc("GET /api/recipes", requireUser(handleGapRecipes))
	return mux
}

func handleChat(w http.ResponseWriter, r *http.Request, _ string) {
	var req struct {
		Messages []message `json:"messages"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	for _, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" { // the browser must not inject system/tool messages
			http.Error(w, "invalid role: "+m.Role, http.StatusBadRequest)
			return
		}
	}
	p, steps, err := runAgent(r.Context(), req.Messages)
	if err != nil {
		log.Printf("agent: %v (steps: %v)", err, steps)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"plan": p, "steps": steps})
}

// loadDotEnv sets KEY=VALUE lines from path; real environment variables win.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && !strings.HasPrefix(k, "#") && os.Getenv(k) == "" {
			os.Setenv(strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
}
