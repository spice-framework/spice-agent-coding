//go:build linux || darwin

package daemonprocess

import (
	"strconv"
	"strings"
)

type unixEnvironment struct{}

func (unixEnvironment) withRegistry(environment []string) []string {
	return (unixEnvironment{}).withFixed(environment, descendantRegistryEnvironment, strconv.Itoa(descendantRegistryFD))
}

func (unixEnvironment) withFixed(environment []string, name, fixedValue string) []string {
	result := (unixEnvironment{}).without(environment, name)
	return append(result, name+"="+fixedValue)
}

func (unixEnvironment) without(environment []string, name string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, prefix) {
			result = append(result, value)
		}
	}
	return result
}
