//go:build windows

package daemonprocess

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"
)

type windowsEnvironment struct{}

func (windowsEnvironment) block(environment []string) ([]uint16, error) {
	environment, err := (windowsEnvironment{}).normalize(environment)
	if err != nil {
		return nil, err
	}
	block := make([]uint16, 0, 2)
	for _, value := range environment {
		block = append(block, utf16.Encode([]rune(value))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	if len(block) == 1 {
		block = append(block, 0)
	}
	return block, nil
}

func (windowsEnvironment) normalize(environment []string) ([]string, error) {
	seen := make(map[string]struct{}, len(environment))
	normalized := make([]string, 0, len(environment))
	for _, value := range slices.Backward(environment) {
		key, ok := (windowsEnvironment{}).key(value)
		if !ok {
			return nil, errors.New("managed daemon Windows environment entry is invalid")
		}
		folded := strings.ToUpper(key)
		if _, duplicate := seen[folded]; duplicate {
			continue
		}
		seen[folded] = struct{}{}
		normalized = append(normalized, value)
	}
	for left, right := 0, len(normalized)-1; left < right; left, right = left+1, right-1 {
		normalized[left], normalized[right] = normalized[right], normalized[left]
	}
	sort.Slice(normalized, func(left, right int) bool {
		leftKey, _ := (windowsEnvironment{}).key(normalized[left])
		rightKey, _ := (windowsEnvironment{}).key(normalized[right])
		return strings.ToUpper(leftKey) < strings.ToUpper(rightKey)
	})
	return normalized, nil
}

func (windowsEnvironment) key(value string) (string, bool) {
	separator := strings.IndexByte(value, '=')
	if separator == 0 {
		next := strings.IndexByte(value[1:], '=')
		if next < 0 {
			return "", false
		}
		separator = next + 1
	}
	if separator < 0 {
		return "", false
	}
	return value[:separator], true
}
