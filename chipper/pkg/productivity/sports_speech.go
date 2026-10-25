package productivity

import (
	"strconv"
	"strings"
)

// Use the compact sports pronunciation for hundreds: 133 is "one thirty
// three" and 104 is "one oh four". Preserve fractional points explicitly.
func spokenSportsNumber(value string) string {
	value = strings.TrimSpace(value)
	whole, fraction, decimal := strings.Cut(value, ".")
	number, err := strconv.Atoi(whole)
	if err != nil || number < 100 || number > 999 {
		return value
	}
	if decimal {
		if fraction == "" {
			return value
		}
		for _, digit := range fraction {
			if digit < '0' || digit > '9' {
				return value
			}
		}
	}
	small := [...]string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	spoken := small[number/100] + " "
	remainder := number % 100
	switch {
	case remainder == 0:
		spoken += "hundred"
	case remainder < 10:
		spoken += "oh " + small[remainder]
	case remainder < 20:
		spoken += small[remainder]
	default:
		tens := [...]string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
		spoken += tens[remainder/10]
		if units := remainder % 10; units != 0 {
			spoken += " " + small[units]
		}
	}
	if decimal {
		spoken += " point"
		for _, digit := range fraction {
			spoken += " " + small[digit-'0']
		}
	}
	return spoken
}
