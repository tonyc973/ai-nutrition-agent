package main

// The nutrients the app tracks. Daily targets and food sources come from the
// NIH Office of Dietary Supplements fact sheets (https://ods.od.nih.gov/factsheets/list-all/),
// which give the official US Dietary Reference Intakes (RDA, or AI where no RDA exists).

type nutrient struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Unit    string   `json:"unit"`
	usda    string   // USDA FoodData Central nutrient number
	spoon   string   // Spoonacular nutrient name (filter param is "min" + name without spaces)
	sources []string // good food sources, from the NIH fact sheet
}

var nutrients = []nutrient{
	{"protein", "Protein", "g", "203", "Protein", []string{"chicken breast", "Greek yogurt", "lentils", "eggs", "tofu", "tuna"}},
	{"fiber", "Fiber", "g", "291", "Fiber", []string{"lentils", "chickpeas", "oats", "raspberries", "pears", "broccoli"}},
	{"vitamin_c", "Vitamin C", "mg", "401", "Vitamin C", []string{"red bell pepper", "orange", "kiwi", "broccoli", "strawberries", "Brussels sprouts"}},
	{"vitamin_d", "Vitamin D", "µg", "328", "Vitamin D", []string{"salmon", "trout", "sardines", "fortified milk", "fortified plant milk", "egg yolk", "UV-exposed mushrooms"}},
	{"calcium", "Calcium", "mg", "301", "Calcium", []string{"yogurt", "milk", "cheese", "fortified plant milk", "calcium-set tofu", "kale"}},
	{"iron", "Iron", "mg", "303", "Iron", []string{"fortified cereal", "lentils", "beef", "spinach", "tofu", "white beans"}},
	{"potassium", "Potassium", "mg", "306", "Potassium", []string{"potatoes", "beans", "bananas", "dried apricots", "spinach", "yogurt"}},
	{"magnesium", "Magnesium", "mg", "304", "Magnesium", []string{"pumpkin seeds", "almonds", "spinach", "black beans", "cashews", "whole wheat bread"}},
}

const usdaEnergy = "208" // kcal, tracked next to the nutrients above

// Daily targets per age group (14-18, 19-30, 31-50, 51-70, 71+) as {male, female}. Protein is per kg, see dailyTarget.
var dri = map[string][5][2]float64{
	"fiber":     {{38, 26}, {38, 25}, {38, 25}, {30, 21}, {30, 21}},
	"vitamin_c": {{75, 65}, {90, 75}, {90, 75}, {90, 75}, {90, 75}},
	"vitamin_d": {{15, 15}, {15, 15}, {15, 15}, {15, 15}, {20, 20}},
	"calcium":   {{1300, 1300}, {1000, 1000}, {1000, 1000}, {1000, 1200}, {1200, 1200}},
	"iron":      {{11, 15}, {8, 18}, {8, 18}, {8, 8}, {8, 8}},
	"potassium": {{3000, 2300}, {3400, 2600}, {3400, 2600}, {3400, 2600}, {3400, 2600}},
	"magnesium": {{410, 360}, {400, 310}, {420, 320}, {420, 320}, {420, 320}},
}

// dailyTarget is how much of a nutrient this person needs per day.
func dailyTarget(key string, p userProfile) float64 {
	if key == "protein" {
		return 0.8 * p.Weight // RDA: 0.8 g per kg of body weight
	}
	group := 4
	for i, maxAge := range []float64{18, 30, 50, 70} {
		if p.Age <= maxAge {
			group = i
			break
		}
	}
	sex := 0
	if p.Sex == "female" {
		sex = 1
	}
	return dri[key][group][sex]
}

func findNutrient(key string) (nutrient, bool) {
	for _, n := range nutrients {
		if n.Key == key {
			return n, true
		}
	}
	return nutrient{}, false
}
