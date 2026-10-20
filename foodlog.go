package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// The food log: the user writes what they ate in plain words, and we turn it into nutrients.
//
//   1. the model splits the text into foods, grams and a USDA search query   (parseMeal)
//   2. the code searches USDA FoodData Central for candidates for each food    (searchUSDA)
//   3. the model picks the right candidate from that closed list              (pickMatches)
//   4. the code scales the USDA values (per 100 g) to the grams eaten         (analyzeMeal)
//
// The model never writes a nutrient number: every number comes from USDA.

var usdaURL = "https://api.nal.usda.gov/fdc/v1/foods/search"

const (
	maxEntriesPerDay = 30
	maxItemsPerEntry = 15
	usdaCandidates   = 6
)

type logEntry struct {
	ID    string    `json:"id"`
	Text  string    `json:"text"` // what the user typed
	Items []logItem `json:"items"`
}

type logItem struct {
	Name      string             `json:"name"`  // the food as the model understood it
	Grams     float64            `json:"grams"` // estimated amount eaten
	Match     string             `json:"match"` // USDA food it was matched to, "" if none fitted
	Nutrients map[string]float64 `json:"nutrients"`
}

var parseFormat = schemaFormat("meal_items", `{
  "type": "object", "additionalProperties": false, "required": ["items"],
  "properties": {"items": {"type": "array", "items": {
    "type": "object", "additionalProperties": false, "required": ["name", "grams", "query"],
    "properties": {"name": {"type": "string"}, "grams": {"type": "number"}, "query": {"type": "string"}}
  }}}
}`)

const parsePrompt = `You turn a description of what someone ate into a list of foods. The text may be in any language.
For each food or drink give:
- name: a short English name, e.g. "cheese pizza"
- grams: your best estimate of the amount eaten, in grams (for drinks 1 ml = 1 g). Use typical portions when no amount is given.
- query: a search query for the USDA FNDDS database, worded the way FNDDS names foods, e.g. "pizza cheese regular crust", "soft drink cola", "apple raw", "coffee brewed", "chicken breast grilled".
Split dishes into separate foods when people eat them as separate foods ("chicken with rice" -> chicken, rice). Ignore anything that is not food or drink.`

var pickFormat = schemaFormat("food_matches", `{
  "type": "object", "additionalProperties": false, "required": ["picks"],
  "properties": {"picks": {"type": "array", "items": {"type": "integer"}}}
}`)

const pickPrompt = `For each numbered food, choose the USDA candidate that best matches what was eaten.
Answer with one candidate number per food, in the same order. Use -1 when no candidate is a reasonable match.`

// analyzeMeal turns free text into foods with nutrients.
func analyzeMeal(ctx context.Context, text string) ([]logItem, error) {
	var parsed struct {
		Items []struct {
			Name  string  `json:"name"`
			Grams float64 `json:"grams"`
			Query string  `json:"query"`
		} `json:"items"`
	}
	if err := askJSON(ctx, parsePrompt, text, parseFormat, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Items) == 0 {
		return nil, errors.New("no food or drink found in that text")
	}
	parsed.Items = parsed.Items[:min(len(parsed.Items), maxItemsPerEntry)]

	candidates := make([][]usdaFood, len(parsed.Items))
	var list strings.Builder
	for i, it := range parsed.Items {
		foods, err := searchUSDA(ctx, it.Query)
		if err != nil {
			return nil, err
		}
		candidates[i] = foods
		fmt.Fprintf(&list, "Food %d: %s\n", i, it.Name)
		for j, f := range foods {
			fmt.Fprintf(&list, "  %d. %s\n", j, f.Description)
		}
	}
	var picked struct {
		Picks []int `json:"picks"`
	}
	if err := askJSON(ctx, pickPrompt, list.String(), pickFormat, &picked); err != nil {
		return nil, err
	}

	items := make([]logItem, len(parsed.Items))
	for i, it := range parsed.Items {
		items[i] = logItem{Name: it.Name, Grams: math.Round(min(max(it.Grams, 1), 3000)), Nutrients: map[string]float64{}}
		if i >= len(picked.Picks) || picked.Picks[i] < 0 || picked.Picks[i] >= len(candidates[i]) {
			continue // no trustworthy match: keep the food, but with no nutrients
		}
		f := candidates[i][picked.Picks[i]]
		items[i].Match = f.Description
		for key, per100g := range f.per100g {
			items[i].Nutrients[key] = math.Round(per100g*items[i].Grams) / 100 // per 100 g -> per portion, 2 decimals
		}
	}
	return items, nil
}

type usdaFood struct {
	Description string
	per100g     map[string]float64 // nutrient key, or "kcal" -> amount in 100 g
}

var usdaCache sync.Map // query -> []usdaFood; the USDA rate limit is small, so never ask twice

// searchUSDA returns the top FNDDS ("foods as eaten") matches, with the nutrients we track.
func searchUSDA(ctx context.Context, query string) ([]usdaFood, error) {
	if cached, ok := usdaCache.Load(query); ok {
		return cached.([]usdaFood), nil
	}
	v := url.Values{"query": {query}, "dataType": {"Survey (FNDDS)"}, "pageSize": {fmt.Sprint(usdaCandidates)}}
	req, _ := http.NewRequestWithContext(ctx, "GET", usdaURL+"?"+v.Encode(), nil)
	req.Header.Set("X-Api-Key", cmp.Or(os.Getenv("USDA_API_KEY"), "DEMO_KEY")) // header, so the key never shows up in errors
	var out struct {
		Foods []struct {
			Description   string `json:"description"`
			FoodNutrients []struct {
				Number string  `json:"nutrientNumber"`
				Value  float64 `json:"value"`
			} `json:"foodNutrients"`
		} `json:"foods"`
	}
	if err := doJSON(req, &out); err != nil {
		if isStatus(err, http.StatusTooManyRequests) {
			return nil, errors.New("the USDA food database rate limit is reached; try again in an hour, or set your own free USDA_API_KEY")
		}
		return nil, fmt.Errorf("USDA search for %q: %w", query, err)
	}
	keyOf := map[string]string{usdaEnergy: "kcal"}
	for _, n := range nutrients {
		keyOf[n.usda] = n.Key
	}
	foods := []usdaFood{}
	for _, f := range out.Foods {
		food := usdaFood{f.Description, map[string]float64{}}
		for _, n := range f.FoodNutrients {
			if key, ok := keyOf[n.Number]; ok {
				food.per100g[key] = n.Value
			}
		}
		foods = append(foods, food)
	}
	usdaCache.Store(query, foods)
	return foods, nil
}

func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// GET /api/log?date=2026-10-20
func handleGetLog(w http.ResponseWriter, r *http.Request, username string) {
	date := r.URL.Query().Get("date")
	if !validDate(date) {
		http.Error(w, "date must look like 2026-10-20", http.StatusBadRequest)
		return
	}
	mu.Lock()
	entries := slices.Clone(users[username].Log[date])
	mu.Unlock()
	if entries == nil {
		entries = []logEntry{} // JSON [] instead of null
	}
	writeJSON(w, map[string]any{"date": date, "entries": entries})
}

// POST /api/log {"date": "2026-10-20", "text": "2 slices of pizza and a cola"}
func handleAddLog(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Date string `json:"date"`
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !validDate(req.Date) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || len(req.Text) > 500 {
		http.Error(w, "describe what you ate in 1-500 characters", http.StatusBadRequest)
		return
	}
	mu.Lock()
	full := len(users[username].Log[req.Date]) >= maxEntriesPerDay
	mu.Unlock()
	if full {
		http.Error(w, fmt.Sprintf("a day can hold at most %d entries", maxEntriesPerDay), http.StatusBadRequest)
		return
	}

	items, err := analyzeMeal(r.Context(), req.Text) // slow (model + USDA), so it runs outside the lock
	if err != nil {
		log.Println("food log:", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	entry := logEntry{ID: randomID(), Text: req.Text, Items: items}

	mu.Lock()
	u := users[username]
	if u.Log == nil {
		u.Log = map[string][]logEntry{}
	}
	u.Log[req.Date] = append(u.Log[req.Date], entry)
	if err = saveUsers(); err != nil {
		u.Log[req.Date] = u.Log[req.Date][:len(u.Log[req.Date])-1]
	}
	mu.Unlock()
	if err != nil {
		http.Error(w, "could not save the entry", http.StatusInternalServerError)
		return
	}
	writeJSON(w, entry)
}

// DELETE /api/log/{date}/{id}
func handleDeleteLog(w http.ResponseWriter, r *http.Request, username string) {
	date, id := r.PathValue("date"), r.PathValue("id")
	mu.Lock()
	u := users[username]
	old := u.Log[date]
	i := slices.IndexFunc(old, func(e logEntry) bool { return e.ID == id })
	var err error
	if i >= 0 {
		u.Log[date] = slices.Delete(slices.Clone(old), i, i+1)
		if len(u.Log[date]) == 0 {
			delete(u.Log, date)
		}
		if err = saveUsers(); err != nil {
			u.Log[date] = old
		}
	}
	mu.Unlock()
	switch {
	case i < 0:
		http.Error(w, "entry not found", http.StatusNotFound)
	case err != nil:
		http.Error(w, "could not delete the entry", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
