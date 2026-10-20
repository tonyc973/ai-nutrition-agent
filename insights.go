package main

import (
	"cmp"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Insights compare what the user ate in the last days with their official daily targets.
// The numbers are computed in code (computeInsights); the model only explains them (handleAdvice).

const (
	insightDays = 7
	gapPercent  = 80 // below this share of the target, a nutrient counts as missing
	maxTips     = 4  // explain only the biggest gaps, so the advice stays readable
)

type insights struct {
	From      string         `json:"from"`
	To        string         `json:"to"`
	Days      int            `json:"days"` // days in the window with at least one entry
	Kcal      float64        `json:"kcal"` // average per logged day
	Nutrients []nutrientStat `json:"nutrients"`
	Foods     []string       `json:"foods"` // what was eaten, most frequent first
}

// computeInsights averages intake over the logged days only: a day the user forgot to log is not a day they ate nothing.
func computeInsights(foodLog map[string][]logEntry, p userProfile, today time.Time) insights {
	in := insights{From: today.AddDate(0, 0, -(insightDays - 1)).Format(time.DateOnly), To: today.Format(time.DateOnly)}
	total := map[string]float64{}
	eaten := map[string]int{}
	for date, entries := range foodLog {
		if date < in.From || date > in.To || len(entries) == 0 { // YYYY-MM-DD strings sort like dates
			continue
		}
		in.Days++
		for _, e := range entries {
			for _, it := range e.Items {
				eaten[it.Name]++
				for key, amount := range it.Nutrients {
					total[key] += amount
				}
			}
		}
	}
	days := float64(max(in.Days, 1))
	in.Kcal = math.Round(total["kcal"] / days)
	for _, n := range nutrients {
		target := dailyTarget(n.Key, p)
		avg := math.Round(total[n.Key]/days*10) / 10
		in.Nutrients = append(in.Nutrients, nutrientStat{n, target, avg, int(math.Round(avg / target * 100))})
	}
	for name := range eaten {
		in.Foods = append(in.Foods, name)
	}
	slices.SortFunc(in.Foods, func(a, b string) int {
		return cmp.Or(eaten[b]-eaten[a], strings.Compare(a, b)) // most eaten first, then alphabetical
	})
	in.Foods = in.Foods[:min(len(in.Foods), 25)]
	return in
}

// userInsights loads the profile and log of a user; ok is false (and an error was written) without a profile.
func userInsights(w http.ResponseWriter, username string) (insights, userProfile, bool) {
	mu.Lock()
	defer mu.Unlock()
	u := users[username]
	if u.Profile == nil {
		http.Error(w, "complete your profile first", http.StatusBadRequest)
		return insights{}, userProfile{}, false
	}
	return computeInsights(u.Log, *u.Profile, time.Now()), *u.Profile, true
}

// GET /api/insights: the numbers, instantly.
func handleInsights(w http.ResponseWriter, r *http.Request, username string) {
	if in, _, ok := userInsights(w, username); ok {
		writeJSON(w, in)
	}
}

var adviceFormat = schemaFormat("insights_advice", `{
  "type": "object", "additionalProperties": false, "required": ["summary", "tips"],
  "properties": {
    "summary": {"type": "string"},
    "tips": {"type": "array", "items": {
      "type": "object", "additionalProperties": false, "required": ["nutrient", "advice", "foods"],
      "properties": {"nutrient": {"type": "string"}, "advice": {"type": "string"}, "foods": {"type": "array", "items": {"type": "string"}}}
    }}
  }
}`)

const advicePrompt = `You are a friendly nutrition coach. You get a summary of someone's food log for the last days:
the foods they ate, and their average daily intake of each nutrient compared with their official daily target (NIH Dietary Reference Intakes).
Write in English. Be specific and kind; no medical claims, no supplements.
- summary: 2-3 sentences. Name the patterns you see in the foods they actually ate (e.g. "mostly pizza and soft drinks, few vegetables or fruit") and what that means for the biggest gaps. If nothing is missing, say what they do well.
- tips: exactly one tip per nutrient marked missing, in the order given. "nutrient" is the nutrient key. "advice" is one sentence that ties the gap to what they ate.
  "foods" are 3 foods copied exactly from that nutrient's sources list, choosing only foods that fit the person's diet and intolerances.`

// GET /api/insights/advice: the model's explanation of the numbers (a few seconds).
func handleAdvice(w http.ResponseWriter, r *http.Request, username string) {
	in, p, ok := userInsights(w, username)
	if !ok {
		return
	}
	type tip struct {
		Nutrient string   `json:"nutrient"`
		Advice   string   `json:"advice"`
		Foods    []string `json:"foods"`
	}
	var advice struct {
		Summary string `json:"summary"`
		Tips    []tip  `json:"tips"`
	}
	if in.Days == 0 {
		writeJSON(w, map[string]any{"summary": "", "tips": []tip{}})
		return
	}

	type row struct {
		Key     string   `json:"key"`
		Percent int      `json:"percent_of_target"`
		Missing bool     `json:"missing"`
		Sources []string `json:"sources"`
	}
	// The code decides what is missing (the biggest gaps, at most maxTips); the model only explains it.
	byGap := slices.Clone(in.Nutrients)
	slices.SortStableFunc(byGap, func(a, b nutrientStat) int { return a.Percent - b.Percent })
	var rows []row
	missing := map[string]bool{}
	for _, n := range byGap {
		isGap := n.Percent < gapPercent && len(missing) < maxTips
		if isGap {
			missing[n.Key] = true
		}
		rows = append(rows, row{n.Key, n.Percent, isGap, n.sources})
	}
	input, _ := json.Marshal(map[string]any{
		"diet": p.Diet, "intolerances": p.Intolerances, "goal": p.Goal,
		"days_logged": in.Days, "foods_eaten": in.Foods, "nutrients": rows,
	})
	if err := askJSON(r.Context(), advicePrompt, string(input), adviceFormat, &advice); err != nil {
		log.Println("advice:", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Trust boundary: keep only tips for the gaps we picked, and only foods from the NIH source lists.
	tips := []tip{}
	for _, t := range advice.Tips {
		n, ok := findNutrient(t.Nutrient)
		if !ok || !missing[t.Nutrient] {
			continue
		}
		t.Foods = slices.DeleteFunc(t.Foods, func(f string) bool {
			return !slices.ContainsFunc(n.sources, func(s string) bool { return strings.EqualFold(s, f) })
		})
		tips = append(tips, t)
	}
	writeJSON(w, map[string]any{"summary": advice.Summary, "tips": tips})
}

type gapRecipe struct {
	recipe
	Amount float64 `json:"amount"` // of the requested nutrient, per serving
}

// GET /api/recipes?nutrient=vitamin_c: recipes rich in a nutrient that respect the user's diet and intolerances.
func handleGapRecipes(w http.ResponseWriter, r *http.Request, username string) {
	n, ok := findNutrient(r.URL.Query().Get("nutrient"))
	if !ok {
		http.Error(w, "unknown nutrient", http.StatusBadRequest)
		return
	}
	mu.Lock()
	p := users[username].Profile
	mu.Unlock()
	if p == nil {
		http.Error(w, "complete your profile first", http.StatusBadRequest)
		return
	}
	perServing := math.Ceil(0.25 * dailyTarget(n.Key, *p)) // one serving covers at least a quarter of the day
	found, err := searchRecipes(r.Context(), searchQuery{
		Diet: p.Diet, Intolerances: strings.Join(p.Intolerances, ","), Number: 3,
		extra: url.Values{"min" + strings.ReplaceAll(n.spoon, " ", ""): {strconv.Itoa(int(perServing))}},
	})
	if err != nil {
		log.Println("gap recipes:", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out := []gapRecipe{}
	for _, rec := range found {
		out = append(out, gapRecipe{rec, math.Round(rec.nutrients[n.spoon]*10) / 10})
	}
	writeJSON(w, map[string]any{"nutrient": n.Key, "recipes": out})
}
