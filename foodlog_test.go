package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDailyTarget(t *testing.T) {
	cases := []struct {
		sex  string
		age  float64
		key  string
		want float64
	}{
		{"female", 25, "iron", 18}, {"female", 60, "iron", 8}, {"male", 25, "iron", 8},
		{"female", 25, "vitamin_c", 75}, {"male", 40, "vitamin_c", 90},
		{"female", 60, "calcium", 1200}, {"male", 60, "calcium", 1000}, {"male", 16, "calcium", 1300},
		{"female", 75, "vitamin_d", 20}, {"male", 30, "magnesium", 400}, {"male", 31, "magnesium", 420},
	}
	for _, c := range cases {
		if got := dailyTarget(c.key, userProfile{Sex: c.sex, Age: c.age, Weight: 70}); got != c.want {
			t.Errorf("%s %v %s = %v, want %v", c.sex, c.age, c.key, got, c.want)
		}
	}
	if got := dailyTarget("protein", userProfile{Weight: 70}); got != 56 {
		t.Errorf("protein for 70 kg = %v, want 56", got)
	}
}

// Averages count only logged days, and days outside the 7-day window are ignored.
func TestComputeInsights(t *testing.T) {
	today := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	pizza := logItem{Name: "pizza", Nutrients: map[string]float64{"kcal": 800, "vitamin_c": 10}}
	kiwi := logItem{Name: "kiwi", Nutrients: map[string]float64{"kcal": 60, "vitamin_c": 70}}
	foodLog := map[string][]logEntry{
		"2026-10-20": {{Items: []logItem{pizza, kiwi}}},
		"2026-10-14": {{Items: []logItem{pizza}}},
		"2026-10-13": {{Items: []logItem{kiwi, kiwi, kiwi}}}, // 7 days back: outside the window
	}
	in := computeInsights(foodLog, userProfile{Sex: "female", Age: 25, Weight: 60}, today)
	if in.Days != 2 || in.Kcal != 830 || in.From != "2026-10-14" {
		t.Fatalf("days=%d kcal=%v from=%s", in.Days, in.Kcal, in.From)
	}
	vitC := in.Nutrients[2]
	if vitC.Key != "vitamin_c" || vitC.Amount != 45 || vitC.Target != 75 || vitC.Percent != 60 {
		t.Fatalf("vitamin C: %+v", vitC)
	}
	if strings.Join(in.Foods, ",") != "pizza,kiwi" {
		t.Fatalf("foods should be most eaten first: %v", in.Foods)
	}
}

// The whole food log over HTTP, with fake OpenAI and USDA servers:
// add an entry (one food matched, one not), read it back, get insights and advice, delete it.
func TestFoodLogAPI(t *testing.T) {
	usersFile = filepath.Join(t.TempDir(), "users.json")
	users = map[string]*user{}
	t.Setenv("JWT_SECRET", strings.Repeat("s", 32))

	usda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("dataType") != "Survey (FNDDS)" || r.Header.Get("X-Api-Key") == "" {
			t.Errorf("bad USDA request: %s", r.URL)
		}
		w.Write([]byte(`{"foods":[
			{"description":"Whiskey and cola","foodNutrients":[{"nutrientNumber":"208","value":200}]},
			{"description":"Soft drink, cola","foodNutrients":[{"nutrientNumber":"208","value":40},{"nutrientNumber":"401","value":0}]}]}`))
	}))
	defer usda.Close()
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Format struct {
				Schema struct{ Name string } `json:"json_schema"`
			} `json:"response_format"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		answer := map[string]string{
			"meal_items":   `{"items":[{"name":"cola","grams":330,"query":"soft drink cola"},{"name":"mystery stew","grams":250,"query":"mystery stew"}]}`,
			"food_matches": `{"picks":[1,-1]}`,
			// "vitamin_x" and "candy" are not in our lists: the server must drop them.
			"insights_advice": `{"summary":"Mostly cola.","tips":[{"nutrient":"vitamin_c","advice":"Add fruit.","foods":["kiwi","candy"]},{"nutrient":"vitamin_x","advice":"?","foods":[]}]}`,
		}[body.Format.Schema.Name]
		b, _ := json.Marshal(answer)
		w.Write([]byte(`{"choices":[{"message":{"content":` + string(b) + `}}]}`))
	}))
	defer ai.Close()
	usdaURL, openaiURL = usda.URL, ai.URL

	srv := httptest.NewServer(routes())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	call := func(method, path, body string, out any) int {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if out != nil {
			json.Unmarshal(b, out)
		}
		return res.StatusCode
	}

	today := time.Now().Format(time.DateOnly)
	if got := call("POST", "/api/log", `{"date":"`+today+`","text":"a cola"}`, nil); got != 401 {
		t.Fatalf("log without login: %d", got)
	}
	call("POST", "/api/register", `{"username":"ana","password":"secret123"}`, nil)
	if got := call("GET", "/api/insights", "", nil); got != 400 {
		t.Fatalf("insights before the profile is set: %d", got)
	}
	call("PUT", "/api/profile", `{"sex":"female","age":25,"weight":60,"height":165,"activity":"light","goal":"maintain"}`, nil)
	for _, bad := range []string{`{"date":"20-10-2026","text":"cola"}`, `{"date":"` + today + `","text":"   "}`} {
		if got := call("POST", "/api/log", bad, nil); got != 400 {
			t.Fatalf("bad entry %s: %d", bad, got)
		}
	}

	var entry logEntry
	if got := call("POST", "/api/log", `{"date":"`+today+`","text":"a can of cola and some stew"}`, &entry); got != 200 {
		t.Fatalf("add entry: %d", got)
	}
	cola, stew := entry.Items[0], entry.Items[1]
	if cola.Match != "Soft drink, cola" || cola.Nutrients["kcal"] != 132 { // 40 kcal/100 g * 330 g
		t.Fatalf("cola: %+v", cola)
	}
	if stew.Match != "" || len(stew.Nutrients) != 0 {
		t.Fatalf("an unmatched food must have no nutrients: %+v", stew)
	}

	var day struct{ Entries []logEntry }
	call("GET", "/api/log?date="+today, "", &day)
	if len(day.Entries) != 1 || day.Entries[0].ID != entry.ID {
		t.Fatalf("log for today: %+v", day)
	}
	var in insights
	call("GET", "/api/insights", "", &in)
	if in.Days != 1 || in.Kcal != 132 {
		t.Fatalf("insights: %+v", in)
	}
	var advice struct {
		Tips []struct {
			Nutrient string
			Foods    []string
		}
	}
	call("GET", "/api/insights/advice", "", &advice)
	if len(advice.Tips) != 1 || advice.Tips[0].Nutrient != "vitamin_c" || strings.Join(advice.Tips[0].Foods, ",") != "kiwi" {
		t.Fatalf("advice must keep only known nutrients and NIH foods: %+v", advice)
	}

	if got := call("DELETE", "/api/log/"+today+"/"+entry.ID, "", nil); got != 204 {
		t.Fatalf("delete: %d", got)
	}
	if got := call("DELETE", "/api/log/"+today+"/"+entry.ID, "", nil); got != 404 {
		t.Fatalf("delete twice: %d", got)
	}
}

// A USDA rate limit becomes a message the user can act on, not a raw HTTP dump.
func TestUSDARateLimit(t *testing.T) {
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"OVER_RATE_LIMIT"}}`, http.StatusTooManyRequests)
	}))
	defer limited.Close()
	usdaURL = limited.URL
	if _, err := searchUSDA(t.Context(), "rate limit test"); err == nil || !strings.Contains(err.Error(), "USDA_API_KEY") {
		t.Fatalf("want a readable rate-limit error, got %v", err)
	}
}
