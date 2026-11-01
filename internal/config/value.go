package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// parse reads a value for key k from TOML (string, int64, float64, bool)
// or from ROAR_SET (always a string).
func parse(k Key, raw any) (any, error) {
	v, err := parseKind(k.Kind, raw)
	if err != nil {
		return nil, err
	}
	if err := check(k, v); err != nil {
		return nil, err
	}
	return v, nil
}

func parseKind(kind Kind, raw any) (any, error) {
	s, isString := raw.(string)
	s = strings.TrimSpace(s)
	switch kind {
	case KindDuration:
		if !isString {
			return nil, errors.New("want a duration such as 750ms or 2m")
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, errors.New("want a duration such as 750ms or 2m")
		}
		return d, nil
	case KindSize, KindRate:
		if n, ok := raw.(int64); ok {
			return n, nil
		}
		if !isString {
			return nil, errors.New("want a size such as 768KiB")
		}
		if kind == KindRate {
			var ok bool
			if s, ok = strings.CutSuffix(s, "/s"); !ok {
				return nil, errors.New("want a rate such as 256KiB/s")
			}
		}
		return parseSize(s)
	case KindFloat:
		switch n := raw.(type) {
		case float64:
			return n, nil
		case int64:
			return float64(n), nil
		}
		if f, err := strconv.ParseFloat(s, 64); isString && err == nil {
			return f, nil
		}
		return nil, errors.New("want a number")
	case KindInt:
		if n, ok := raw.(int64); ok {
			return int(n), nil
		}
		if n, err := strconv.Atoi(s); isString && err == nil {
			return n, nil
		}
		return nil, errors.New("want a whole number")
	case KindBool:
		if b, ok := raw.(bool); ok {
			return b, nil
		}
		if b, err := strconv.ParseBool(s); isString && err == nil {
			return b, nil
		}
		return nil, errors.New("want true or false")
	}
	return nil, fmt.Errorf("unknown kind %d", kind)
}

var units = []struct {
	suffix string
	scale  int64
}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}

func parseSize(s string) (int64, error) {
	scale := int64(1)
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			s, scale = strings.TrimSpace(n), u.scale
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f*float64(scale) > math.MaxInt64 {
		return 0, errors.New("want a size such as 768KiB")
	}
	return int64(f * float64(scale)), nil
}

func check(k Key, v any) error {
	var f float64
	switch n := v.(type) {
	case time.Duration:
		f = float64(n)
	case int64:
		f = float64(n)
	case int:
		f = float64(n)
	case float64:
		f = n
	default:
		return nil
	}
	switch k.Check {
	case Positive:
		if f <= 0 {
			return errors.New("must be above 0")
		}
	case NonNegative:
		if f < 0 {
			return errors.New("must not be negative")
		}
	case Factor:
		if f < 1 {
			return errors.New("must be at least 1")
		}
	case Fraction:
		if f < 0 || f > 1 {
			return errors.New("must be from 0 to 1")
		}
	case Alpha:
		if f <= 0 || f > 1 {
			return errors.New("must be above 0 and at most 1")
		}
	}
	return nil
}
