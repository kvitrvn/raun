package jobtest

import "testing"

func TestMemory(t *testing.T) {
	Suite(t, func(t *testing.T) Fixture {
		return Fixture{Store: &Memory{}, AddRepository: func(*testing.T, string, string, string) {}}
	})
}
