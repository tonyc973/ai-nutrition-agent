package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"
)

var (
	openaiURL  = "https://api.openai.com/v1/chat/completions"
	httpClient = &http.Client{Timeout: 90 * time.Second}
)

const (
	maxSteps    = 10
	maxSearches = 6 // Spoonacular's free tier is 50 points/day, ~1-2 per search
)

const systemPrompt = `You are a friendly virtual nutritionist. Always answer in English.
How you work:
1. When you receive the user's details, call calculate_targets. Never calculate calories yourself.
2. Search recipes with search_recipes, exactly 3 searches: breakfast (type "breakfast", ~25% of calories),
   one search for both lunch and dinner (type "main course", ~32% each, number 5) and a snack (type "snack", ~11%).
   For every search set min_calories and max_calories to ±20% of that meal's budget.
   On EVERY search pass the user's diet and all of their intolerances (they are allergies, not preferences). Respect their preferences too.
   You have at most 6 searches per request; never repeat a search you already made. If a search returns nothing, relax the filters (drop the query or widen the range) and try again.
3. The plan has exactly 4 meals: Breakfast, Lunch, Dinner, Snack. The day's total must be within ±10% of the calorie target.
   Only use recipe_id values returned by search_recipes. The "meal" field is one of: Breakfast, Lunch, Dinner, Snack.
   In "why" briefly explain (one sentence) why the recipe fits. In "message" give 2-3 sentences about the plan and the targets.
For change requests, keep the rest of the plan and change only what the user asks for.`

// The final answer must match this schema, so the UI always gets the same shape.
var planFormat = json.RawMessage(`{
  "type": "json_schema",
  "json_schema": {
    "name": "meal_plan",
    "strict": true,
    "schema": {
      "type": "object",
      "additionalProperties": false,
      "required": ["message", "targets", "meals"],
      "properties": {
        "message": {"type": "string"},
        "targets": {
          "type": "object",
          "additionalProperties": false,
          "required": ["calories", "protein_g", "carbs_g", "fat_g"],
          "properties": {
            "calories": {"type": "integer"},
            "protein_g": {"type": "integer"},
            "carbs_g": {"type": "integer"},
            "fat_g": {"type": "integer"}
          }
        },
        "meals": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["recipe_id", "meal", "why"],
            "properties": {
              "recipe_id": {"type": "integer"},
              "meal": {"type": "string"},
              "why": {"type": "string"}
            }
          }
        }
      }
    }
  }
}`)

type plan struct {
	Message string `json:"message"`
	Targets struct {
		Calories int `json:"calories"`
		ProteinG int `json:"protein_g"`
		CarbsG   int `json:"carbs_g"`
		FatG     int `json:"fat_g"`
	} `json:"targets"`
	Meals []meal `json:"meals"`
}

type meal struct {
	RecipeID int     `json:"recipe_id"`
	Meal     string  `json:"meal"`
	Why      string  `json:"why"`
	Recipe   *recipe `json:"recipe,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type step struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
}

// runAgent is the tool-calling loop: ask the model, run the tools it wants, repeat until it answers.
func runAgent(ctx context.Context, history []message) (*plan, []step, error) {
	msgs := []any{message{"system", systemPrompt}}
	for _, m := range history {
		msgs = append(msgs, m)
	}
	var steps []step
	reviewed, searches := false, 0
	for i := range maxSteps {
		// Out of searches or steps: force a final answer instead of letting the model keep calling tools.
		mustAnswer := searches >= maxSearches || i == maxSteps-1
		content, calls, err := callOpenAI(ctx, msgs, mustAnswer)
		if err != nil {
			return nil, steps, err
		}
		if len(calls) == 0 {
			p, err := finalPlan(content)
			if fix := reviewPlan(p); err == nil && fix != "" && !reviewed { // one correction round, to bound cost
				reviewed = true
				steps = append(steps, step{"review_plan", fix})
				msgs = append(msgs, message{"assistant", content}, message{"user", fix})
				continue
			}
			return p, steps, err
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "tool_calls": calls})
		for _, c := range calls {
			steps = append(steps, step{c.Function.Name, c.Function.Arguments})
			if c.Function.Name == "search_recipes" {
				searches++
			}
			out := fmt.Sprintf("error: you used all %d searches; choose from the recipes you already found", maxSearches)
			if c.Function.Name != "search_recipes" || searches <= maxSearches {
				out = runTool(ctx, c.Function.Name, c.Function.Arguments)
			}
			msgs = append(msgs, map[string]string{"role": "tool", "tool_call_id": c.ID, "content": out})
		}
	}
	return nil, steps, fmt.Errorf("the agent did not finish within %d steps", maxSteps)
}

// finalPlan parses the model's answer and attaches the real recipe data, dropping any id the tools never returned.
func finalPlan(content string) (*plan, error) {
	var p plan
	if err := json.Unmarshal([]byte(content), &p); err != nil {
		return nil, fmt.Errorf("invalid answer from the model: %w", err)
	}
	meals := p.Meals[:0]
	for _, m := range p.Meals {
		if r, ok := recipeCache.Load(m.RecipeID); ok {
			rec := r.(recipe)
			m.Recipe = &rec
			meals = append(meals, m)
		}
	}
	p.Meals = meals
	return &p, nil
}

// reviewPlan returns what to fix when the plan misses meals or is far from the calorie target, or "" if it is fine.
func reviewPlan(p *plan) string {
	if p == nil {
		return ""
	}
	total := 0
	for _, m := range p.Meals {
		total += m.Recipe.Calories
	}
	if len(p.Meals) < 4 || math.Abs(float64(total-p.Targets.Calories)) > 0.15*float64(p.Targets.Calories) {
		return fmt.Sprintf("Automatic check: the plan has %d meals and %d kcal, but it must have 4 meals (Breakfast, Lunch, Dinner, Snack) and ~%d kcal. Fix the plan (you may search for recipes again).",
			len(p.Meals), total, p.Targets.Calories)
	}
	return ""
}

func callOpenAI(ctx context.Context, msgs []any, mustAnswer bool) (string, []toolCall, error) {
	body, _ := json.Marshal(map[string]any{
		"model":           cmp.Or(os.Getenv("OPENAI_MODEL"), "gpt-4o-mini"),
		"messages":        msgs,
		"tools":           toolDefs,
		"tool_choice":     map[bool]string{true: "none", false: "auto"}[mustAnswer],
		"response_format": planFormat,
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", openaiURL, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				Refusal   string     `json:"refusal"`
				ToolCalls []toolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := doJSON(req, &out); err != nil {
		return "", nil, err
	}
	if len(out.Choices) == 0 {
		return "", nil, errors.New("OpenAI returned no answer")
	}
	m := out.Choices[0].Message
	if m.Refusal != "" {
		return "", nil, errors.New("the model refused: " + m.Refusal)
	}
	return m.Content, m.ToolCalls, nil
}

func doJSON(req *http.Request, out any) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("%s answered %s: %s", req.URL.Host, resp.Status, b)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
