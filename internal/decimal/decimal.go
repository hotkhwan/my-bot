// Package decimal provides the small exact decimal type needed for order input.
package decimal

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"unicode"
)

// Decimal stores a base-10 decimal as an integer coefficient plus scale.
type Decimal struct {
	value *big.Int
	scale int
}

// Zero returns decimal zero.
func Zero() Decimal {
	return Decimal{value: new(big.Int)}
}

// NewFromInt returns a decimal with no fractional scale.
func NewFromInt(value int64) Decimal {
	return Decimal{value: big.NewInt(value)}
}

// Parse converts a plain base-10 decimal string into an exact Decimal.
func Parse(input string) (Decimal, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return Zero(), fmt.Errorf("decimal is empty")
	}

	sign := 1
	switch text[0] {
	case '+':
		text = text[1:]
	case '-':
		sign = -1
		text = text[1:]
	}
	if text == "" {
		return Zero(), fmt.Errorf("decimal has no digits")
	}

	parts := strings.Split(text, ".")
	if len(parts) > 2 {
		return Zero(), fmt.Errorf("decimal has more than one point")
	}

	whole := parts[0]
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if whole == "" && fraction == "" {
		return Zero(), fmt.Errorf("decimal has no digits")
	}

	for _, r := range whole + fraction {
		if !unicode.IsDigit(r) {
			return Zero(), fmt.Errorf("decimal contains non-digit %q", r)
		}
	}

	digits := whole + fraction
	if digits == "" {
		digits = "0"
	}

	value, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Zero(), fmt.Errorf("decimal is invalid")
	}
	if sign < 0 {
		value.Neg(value)
	}

	return normalize(Decimal{value: value, scale: len(fraction)}), nil
}

// MustParse converts a decimal string and panics if it is invalid.
func MustParse(input string) Decimal {
	value, err := Parse(input)
	if err != nil {
		panic(err)
	}
	return value
}

func (d Decimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Decimal) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}

	value, err := Parse(text)
	if err != nil {
		return err
	}
	*d = value
	return nil
}

// String returns a normalized plain decimal string.
func (d Decimal) String() string {
	d = normalize(d)
	if d.value == nil || d.value.Sign() == 0 {
		return "0"
	}

	value := new(big.Int).Set(d.value)
	sign := ""
	if value.Sign() < 0 {
		sign = "-"
		value.Abs(value)
	}

	digits := value.String()
	if d.scale == 0 {
		return sign + digits
	}

	if len(digits) <= d.scale {
		digits = strings.Repeat("0", d.scale-len(digits)+1) + digits
	}

	point := len(digits) - d.scale
	return sign + digits[:point] + "." + digits[point:]
}

// IsZero reports whether the value is zero.
func (d Decimal) IsZero() bool {
	return d.sign() == 0
}

// IsPositive reports whether the value is greater than zero.
func (d Decimal) IsPositive() bool {
	return d.sign() > 0
}

// Abs returns the absolute value.
func (d Decimal) Abs() Decimal {
	d = normalize(d)
	if d.value.Sign() >= 0 {
		return d
	}

	return normalize(Decimal{value: new(big.Int).Abs(d.value), scale: d.scale})
}

// Neg returns the negated value.
func (d Decimal) Neg() Decimal {
	d = normalize(d)
	return normalize(Decimal{value: new(big.Int).Neg(d.value), scale: d.scale})
}

// Cmp compares d and other.
func (d Decimal) Cmp(other Decimal) int {
	left, right := align(d, other)
	return left.Cmp(right)
}

// Equal reports whether both decimals represent the same value.
func (d Decimal) Equal(other Decimal) bool {
	return d.Cmp(other) == 0
}

// Mul returns d multiplied by other.
func (d Decimal) Mul(other Decimal) Decimal {
	d = normalize(d)
	other = normalize(other)

	return normalize(Decimal{
		value: new(big.Int).Mul(d.value, other.value),
		scale: d.scale + other.scale,
	})
}

// Add returns d plus other, preserving exactness.
func (d Decimal) Add(other Decimal) Decimal {
	left, right := align(d, other)
	return normalize(Decimal{value: new(big.Int).Add(left, right), scale: commonScale(d, other)})
}

// Sub returns d minus other, preserving exactness.
func (d Decimal) Sub(other Decimal) Decimal {
	left, right := align(d, other)
	return normalize(Decimal{value: new(big.Int).Sub(left, right), scale: commonScale(d, other)})
}

// commonScale is the aligned scale used by Add/Sub: the larger of the two
// normalized scales, matching how align widens the coefficients.
func commonScale(a Decimal, b Decimal) int {
	a = normalize(a)
	b = normalize(b)
	if a.scale > b.scale {
		return a.scale
	}
	return b.scale
}

// QuoFloor divides d by divisor and floors the result to scale decimal places.
func (d Decimal) QuoFloor(divisor Decimal, scale int) (Decimal, error) {
	d = normalize(d)
	divisor = normalize(divisor)
	if divisor.sign() == 0 {
		return Zero(), fmt.Errorf("decimal division by zero")
	}
	if scale < 0 {
		return Zero(), fmt.Errorf("decimal scale must be non-negative")
	}

	numerator := new(big.Int).Set(d.value)
	numerator.Mul(numerator, pow10(divisor.scale+scale))

	denominator := new(big.Int).Set(divisor.value)
	denominator.Mul(denominator, pow10(d.scale))

	quotient := new(big.Int).Quo(numerator, denominator)
	return normalize(Decimal{value: quotient, scale: scale}), nil
}

// FloorToStep floors d to the nearest lower multiple of step.
func (d Decimal) FloorToStep(step Decimal) (Decimal, error) {
	d = normalize(d)
	step = normalize(step)
	if step.sign() <= 0 {
		return Zero(), fmt.Errorf("decimal step must be greater than zero")
	}
	if d.sign() < 0 {
		return Zero(), fmt.Errorf("decimal floor to step requires a non-negative value")
	}

	scale := d.scale
	if step.scale > scale {
		scale = step.scale
	}

	value := new(big.Int).Set(d.value)
	if scale > d.scale {
		value.Mul(value, pow10(scale-d.scale))
	}

	stepValue := new(big.Int).Set(step.value)
	if scale > step.scale {
		stepValue.Mul(stepValue, pow10(scale-step.scale))
	}

	quotient := new(big.Int).Quo(value, stepValue)
	floored := new(big.Int).Mul(quotient, stepValue)
	return normalize(Decimal{value: floored, scale: scale}), nil
}

func (d Decimal) sign() int {
	if d.value == nil {
		return 0
	}
	return d.value.Sign()
}

func normalize(d Decimal) Decimal {
	if d.value == nil {
		return Zero()
	}

	value := new(big.Int).Set(d.value)
	scale := d.scale
	if value.Sign() == 0 {
		return Zero()
	}

	ten := big.NewInt(10)
	zero := new(big.Int)
	for scale > 0 {
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(value, ten, remainder)
		if remainder.Cmp(zero) != 0 {
			break
		}
		value = quotient
		scale--
	}

	return Decimal{value: value, scale: scale}
}

func align(a Decimal, b Decimal) (*big.Int, *big.Int) {
	a = normalize(a)
	b = normalize(b)

	left := new(big.Int).Set(a.value)
	right := new(big.Int).Set(b.value)

	switch {
	case a.scale > b.scale:
		right.Mul(right, pow10(a.scale-b.scale))
	case b.scale > a.scale:
		left.Mul(left, pow10(b.scale-a.scale))
	}

	return left, right
}

func pow10(scale int) *big.Int {
	if scale <= 0 {
		return big.NewInt(1)
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
}
