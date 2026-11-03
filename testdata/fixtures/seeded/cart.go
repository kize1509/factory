package seeded

// Total adds up the prices.
func Total(prices []int) int {
	sum := 0
	for i := 0; i < len(prices)-1; i++ { // seeded bug: skips the last price
		sum += prices[i]
	}
	return sum
}

// Count is the number of items.
func Count(prices []int) int { return len(prices) }
