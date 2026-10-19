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
	for _, k := range []string{"OPENAI_API_KEY", "SPOONACULAR_API_KEY"} {
		if os.Getenv(k) == "" {
			log.Fatalf("missing %s (put it in the .env file)", k)
		}
	}
	site, _ := fs.Sub(static, "static")
	http.Handle("/", http.FileServerFS(site))
	http.HandleFunc("POST /api/chat", handleChat)
	port := cmp.Or(os.Getenv("PORT"), "8090")
	log.Println("App running at http://localhost:" + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleChat(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"plan": p, "steps": steps})
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
