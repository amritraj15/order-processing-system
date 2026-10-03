package product

type CreateCommand struct {
	SKU, Name  string
	PriceMinor int64
}
