package money

import (
	"fmt"
	"math/big"
	"order_management/domain/shared"
	"regexp"
)

const MappingVersion = "markets-v1"

var regions = map[string]string{"US": "USD", "IN": "INR", "GB": "GBP", "JP": "JPY", "KW": "KWD", "DE": "EUR", "FR": "EUR"}
var digits = map[string]int{"USD": 2, "INR": 2, "GBP": 2, "JPY": 0, "KWD": 3, "EUR": 2}
var decimal = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,12})?$`)

func Currency(region string) (string, error) {
	c, ok := regions[region]
	if !ok {
		return "", fmt.Errorf("%w: unsupported region", shared.ErrInvalid)
	}
	return c, nil
}
func Digits(currency string) (int, error) {
	d, ok := digits[currency]
	if !ok {
		return 0, fmt.Errorf("%w: unsupported currency", shared.ErrInvalid)
	}
	return d, nil
}
func ParseRate(s string) (*big.Rat, error) {
	if !decimal.MatchString(s) {
		return nil, fmt.Errorf("%w: invalid decimal rate", shared.ErrInvalid)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() <= 0 {
		return nil, fmt.Errorf("%w: rate must be positive", shared.ErrInvalid)
	}
	return r, nil
}
func Convert(amount int64, source, target, rate string) (int64, error) {
	sd, err := Digits(source)
	if err != nil {
		return 0, err
	}
	td, err := Digits(target)
	if err != nil {
		return 0, err
	}
	r, err := ParseRate(rate)
	if err != nil {
		return 0, err
	}
	if amount <= 0 {
		return 0, shared.ErrAmountRange
	}
	ten := big.NewInt(10)
	r.Mul(r, new(big.Rat).SetInt64(amount))
	r.Mul(r, new(big.Rat).SetFrac(new(big.Int).Exp(ten, big.NewInt(int64(td)), nil), new(big.Int).Exp(ten, big.NewInt(int64(sd)), nil)))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	cmp := new(big.Int).Lsh(rem, 1).Cmp(r.Denom())
	if cmp > 0 || (cmp == 0 && q.Bit(0) == 1) {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Sign() <= 0 {
		return 0, shared.ErrAmountRange
	}
	return q.Int64(), nil
}
