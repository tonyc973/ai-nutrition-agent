package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
)

var spoonURL = "https://api.spoonacular.com/recipes/complexSearch"

var toolDefs = json.RawMessage(`[
  {"type": "function", "function": {
    "name": "calculate_targets",
    "description": "Calculates BMI, basal metabolic rate (Mifflin-St Jeor), daily calorie needs and target macronutrients.",
    "parameters": {"type": "object", "required": ["sex", "age", "weight_kg", "height_cm", "activity", "goal"], "properties": {
      "sex": {"type": "string", "enum": ["male", "female"]},
      "age": {"type": "number"},
      "weight_kg": {"type": "number"},
      "height_cm": {"type": "number"},
      "activity": {"type": "string", "enum": ["sedentary", "light", "moderate", "active", "very_active"]},
      "goal": {"type": "string", "enum": ["lose", "maintain", "gain"]}
    }}
  }},
  {"type": "function", "function": {
    "name": "search_recipes",
    "description": "Searches recipes in the Spoonacular database. Returns recipes with calories and macronutrients per serving.",
    "parameters": {"type": "object", "required": ["diet", "intolerances"], "properties": {
      "query": {"type": "string", "description": "Keywords, e.g. 'chicken salad'. Optional."},
      "type": {"type": "string", "enum": ["breakfast", "main course", "salad", "soup", "snack", "side dish", "dessert"]},
      "diet": {"type": "string", "description": "The user's diet: vegetarian, vegan, pescetarian, ketogenic, gluten free, paleo, or \"\" if none"},
      "intolerances": {"type": "string", "description": "ALL of the user's intolerances, comma separated (dairy, egg, gluten, peanut, seafood, shellfish, soy, tree nut, sesame, wheat), or \"\" if none"},
      "exclude_ingredients": {"type": "string", "description": "Ingredients to avoid, comma separated"},
      "min_calories": {"type": "number", "description": "Minimum calories per serving"},
      "max_calories": {"type": "number", "description": "Maximum calories per serving"},
      "min_protein": {"type": "number", "description": "Minimum protein per serving, in grams"},
      "number": {"type": "integer", "description": "How many results (1-5), default 3"}
    }}
  }}
]`)

func runTool(ctx context.Context, name, args string) string {
	var out any
	var err error
	switch name {
	case "calculate_targets":
		var p profile
		if err = json.Unmarshal([]byte(args), &p); err == nil {
			out, err = calcTargets(p)
		}
	case "search_recipes":
		var q searchQuery
		if err = json.Unmarshal([]byte(args), &q); err == nil {
			out, err = searchRecipes(ctx, q)
		}
	default:
		err = fmt.Errorf("unknown tool: %s", name)
	}
	if err != nil {
		return "error: " + err.Error() // the model reads this and can retry or explain
	}
	b, _ := json.Marshal(out)
	return string(b)
}

type profile struct {
	Sex      string  `json:"sex"`
	Age      float64 `json:"age"`
	WeightKg float64 `json:"weight_kg"`
	HeightCm float64 `json:"height_cm"`
	Activity string  `json:"activity"`
	Goal     string  `json:"goal"`
}

type targets struct {
	BMI      float64 `json:"bmi"`
	BMR      int     `json:"bmr"`
	TDEE     int     `json:"tdee"`
	Calories int     `json:"calories"`
	ProteinG int     `json:"protein_g"`
	CarbsG   int     `json:"carbs_g"`
	FatG     int     `json:"fat_g"`
}

var activityFactor = map[string]float64{"sedentary": 1.2, "light": 1.375, "moderate": 1.55, "active": 1.725, "very_active": 1.9}
var goalDelta = map[string]float64{"lose": -500, "maintain": 0, "gain": 300}

func calcTargets(p profile) (targets, error) {
	factor, okA := activityFactor[p.Activity]
	delta, okG := goalDelta[p.Goal]
	if !okA || !okG || (p.Sex != "male" && p.Sex != "female") {
		return targets{}, errors.New("invalid sex, activity or goal")
	}
	if p.Age < 14 || p.Age > 100 || p.WeightKg < 30 || p.WeightKg > 300 || p.HeightCm < 120 || p.HeightCm > 230 {
		return targets{}, errors.New("age (14-100), weight (30-300 kg) or height (120-230 cm) is not realistic")
	}
	bmr := 10*p.WeightKg + 6.25*p.HeightCm - 5*p.Age + 5
	minCalories := 1500.0
	if p.Sex == "female" {
		bmr -= 166 // Mifflin-St Jeor: +5 for men, -161 for women
		minCalories = 1200
	}
	tdee := bmr * factor
	calories := math.Max(tdee+delta, minCalories) // never go below a safe floor
	protein := 1.8 * p.WeightKg
	fat := 0.25 * calories / 9
	carbs := (calories - protein*4 - fat*9) / 4
	return targets{
		BMI:      math.Round(p.WeightKg/math.Pow(p.HeightCm/100, 2)*10) / 10,
		BMR:      int(math.Round(bmr)),
		TDEE:     int(math.Round(tdee)),
		Calories: int(math.Round(calories)),
		ProteinG: int(math.Round(protein)),
		CarbsG:   int(math.Round(carbs)),
		FatG:     int(math.Round(fat)),
	}, nil
}

type searchQuery struct {
	Query              string  `json:"query"`
	Type               string  `json:"type"`
	Diet               string  `json:"diet"`
	Intolerances       string  `json:"intolerances"`
	ExcludeIngredients string  `json:"exclude_ingredients"`
	MinCalories        float64 `json:"min_calories"`
	MaxCalories        float64 `json:"max_calories"`
	MinProtein         float64 `json:"min_protein"`
	Number             int     `json:"number"`

	extra url.Values // extra Spoonacular filters set by our own code, e.g. minVitaminC (the model can't set these)
}

type recipe struct {
	ID             int    `json:"id"`
	Title          string `json:"title"`
	Image          string `json:"image"`
	URL            string `json:"url"`
	ReadyInMinutes int    `json:"ready_in_minutes"`
	Calories       int    `json:"calories"`
	ProteinG       int    `json:"protein_g"`
	CarbsG         int    `json:"carbs_g"`
	FatG           int    `json:"fat_g"`

	nutrients map[string]float64 // every nutrient Spoonacular reports, per serving (not sent to the model)
}

// shortcut: both in-memory caches grow with every search and resets on restart; fine for a demo, use a DB for real users.
var recipeCache sync.Map // recipe id -> recipe
var searchCache sync.Map // request URL -> []recipe; a repeated search costs no Spoonacular points

func searchRecipes(ctx context.Context, q searchQuery) ([]recipe, error) {
	v := url.Values{"addRecipeNutrition": {"true"}, "number": {strconv.Itoa(min(max(cmp.Or(q.Number, 3), 1), 5))}}
	for k, s := range map[string]string{"query": q.Query, "type": q.Type, "diet": q.Diet, "intolerances": q.Intolerances, "excludeIngredients": q.ExcludeIngredients} {
		if s != "" {
			v.Set(k, s)
		}
	}
	if q.MinCalories > 0 {
		v.Set("minCalories", strconv.Itoa(int(q.MinCalories)))
	}
	if q.MaxCalories > 0 {
		v.Set("maxCalories", strconv.Itoa(int(q.MaxCalories)))
	}
	if q.MinProtein > 0 {
		v.Set("minProtein", strconv.Itoa(int(q.MinProtein)))
	}
	for k, vs := range q.extra {
		v[k] = vs
	}
	u := spoonURL + "?" + v.Encode()
	if cached, ok := searchCache.Load(u); ok {
		return cached.([]recipe), nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("x-api-key", os.Getenv("SPOONACULAR_API_KEY")) // header, so the key never shows up in error messages
	var out struct {
		Results []struct {
			ID                   int    `json:"id"`
			Title                string `json:"title"`
			Image                string `json:"image"`
			SourceURL            string `json:"sourceUrl"`
			SpoonacularSourceURL string `json:"spoonacularSourceUrl"`
			ReadyInMinutes       int    `json:"readyInMinutes"`
			Nutrition            struct {
				Nutrients []struct {
					Name   string  `json:"name"`
					Amount float64 `json:"amount"`
				} `json:"nutrients"`
			} `json:"nutrition"`
		} `json:"results"`
	}
	if err := doJSON(req, &out); err != nil {
		if isStatus(err, http.StatusPaymentRequired) {
			return nil, errors.New("the daily Spoonacular recipe quota is used up; it resets tomorrow")
		}
		return nil, err
	}
	recipes := []recipe{}
	for _, r := range out.Results {
		n := map[string]float64{}
		for _, x := range r.Nutrition.Nutrients {
			n[x.Name] = x.Amount
		}
		whole := func(name string) int { return int(math.Round(n[name])) }
		rec := recipe{r.ID, r.Title, r.Image, cmp.Or(r.SourceURL, r.SpoonacularSourceURL), r.ReadyInMinutes,
			whole("Calories"), whole("Protein"), whole("Carbohydrates"), whole("Fat"), n}
		recipeCache.Store(rec.ID, rec)
		recipes = append(recipes, rec)
	}
	searchCache.Store(u, recipes)
	return recipes, nil
}
