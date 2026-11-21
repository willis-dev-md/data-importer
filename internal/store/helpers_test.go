package store_test

import (
	"errors"
	"math"
	"time"
)

func errorsIs(err, target error) bool { return errors.Is(err, target) }

func nanValue() any { return math.NaN() }

func sleep() { time.Sleep(100 * time.Millisecond) }
