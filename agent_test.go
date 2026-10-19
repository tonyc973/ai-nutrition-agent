package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCalcTargets(t *testing.T) {
	got, err := calcTargets(profile{"male", 30, 80, 180, "moderate", "maintain"})
	if err != nil || got.BMR != 1780 || got.Calories != 2759 || got.ProteinG != 144 || got.BMI != 24.7 {
		t.Fatalf("male maintain: %+v %v", got, err)
	}
	// 45 kg woman losing weight would land at ~860 kcal; the floor keeps her at 1200.
	got, _ = calcTargets(profile{"female", 25, 45, 155, "sedentary", "lose"})
	if got.BMR != 1133 || got.Calories != 1200 {
		t.Fatalf("female floor: %+v", got)
	}
	if _, err := calcTargets(profile{"male", 30, 8, 180, "moderate", "maintain"}); err == nil {
		t.Fatal("8 kg should be rejected")
	}
}

// One full loop: model calls a tool, gets Spoonacular data, answers with a too-small plan that includes one invented id.
// The review asks for one correction, then the loop stops even though the plan is still too small.
func TestRunAgent(t *testing.T) {
	spoon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("maxCalories") != "500" || r.Header.Get("x-api-key") == "" {
			t.Errorf("bad spoonacular request: %s", r.URL)
		}
		w.Write([]byte(`{"results":[{"id":42,"title":"Oat Bowl","image":"https://img/42.jpg","sourceUrl":"https://src/42","readyInMinutes":10,
			"nutrition":{"nutrients":[{"name":"Calories","amount":450.4},{"name":"Protein","amount":20},{"name":"Carbohydrates","amount":60},{"name":"Fat","amount":12}]}}]}`))
	}))
	defer spoon.Close()

	turn := 0
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []map[string]any }
		json.NewDecoder(r.Body).Decode(&body)
		turn++
		if turn == 1 {
			w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"c1","type":"function","function":{"name":"search_recipes","arguments":"{\"type\":\"breakfast\",\"max_calories\":500}"}}]}}]}`))
			return
		}
		if last := body.Messages[len(body.Messages)-1]; turn == 2 && (last["role"] != "tool" || last["tool_call_id"] != "c1") {
			t.Errorf("tool result not sent back: %v", last)
		}
		plan := `{"message":"ok","targets":{"calories":2000,"protein_g":120,"carbs_g":220,"fat_g":60},"meals":[{"recipe_id":42,"meal":"Breakfast","why":"x"},{"recipe_id":999,"meal":"Dinner","why":"invented"}]}`
		b, _ := json.Marshal(plan)
		w.Write([]byte(`{"choices":[{"message":{"content":` + string(b) + `}}]}`))
	}))
	defer ai.Close()

	spoonURL, openaiURL = spoon.URL, ai.URL
	t.Setenv("SPOONACULAR_API_KEY", "test")
	p, steps, err := runAgent(context.Background(), []message{{"user", "salut"}})
	if err != nil {
		t.Fatal(err)
	}
	if turn != 3 || len(steps) != 2 || steps[0].Tool != "search_recipes" || steps[1].Tool != "review_plan" {
		t.Fatalf("steps: %+v", steps)
	}
	if len(p.Meals) != 1 || p.Meals[0].Recipe.Title != "Oat Bowl" || p.Meals[0].Recipe.Calories != 450 {
		t.Fatalf("plan should keep only the real recipe: %+v", p.Meals)
	}
}

// The 7th search in one request must not reach Spoonacular.
func TestSearchCap(t *testing.T) {
	hits := 0
	spoon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(`{"results":[]}`))
	}))
	defer spoon.Close()

	var lastTool map[string]any
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages   []map[string]any
			ToolChoice string `json:"tool_choice"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if lastTool == nil {
			lastTool = map[string]any{}
			calls := ""
			for i := range 7 {
				calls += fmt.Sprintf(`%s{"id":"c%d","type":"function","function":{"name":"search_recipes","arguments":"{\"max_calories\":%d}"}}`, map[bool]string{true: ",", false: ""}[i > 0], i, 100+i)
			}
			w.Write([]byte(`{"choices":[{"message":{"tool_calls":[` + calls + `]}}]}`))
			return
		}
		if m := body.Messages[len(body.Messages)-1]; m["role"] == "tool" {
			lastTool = m
			if body.ToolChoice != "none" {
				t.Errorf("after the search cap the model must be forced to answer, got tool_choice=%q", body.ToolChoice)
			}
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"message\":\"\",\"targets\":{\"calories\":0,\"protein_g\":0,\"carbs_g\":0,\"fat_g\":0},\"meals\":[]}"}}]}`))
	}))
	defer ai.Close()

	spoonURL, openaiURL = spoon.URL, ai.URL
	if _, _, err := runAgent(context.Background(), []message{{"user", "salut"}}); err != nil {
		t.Fatal(err)
	}
	if hits != 6 || !strings.HasPrefix(lastTool["content"].(string), "error:") {
		t.Fatalf("hits=%d last tool=%v", hits, lastTool)
	}
}
