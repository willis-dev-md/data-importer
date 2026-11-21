package store_test

import (
	"errors"
	"math"
)

func errorsIs(err, target error) bool { return errors.Is(err, target) }

func nanValue() any { return math.NaN() }
