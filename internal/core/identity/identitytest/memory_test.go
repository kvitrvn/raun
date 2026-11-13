package identitytest

import "testing"

func TestMemory(t *testing.T) {
	Suite(t, func(*testing.T) Fixture {
		m := &Memory{}
		return Fixture{Store: m, StartSession: m.StartSession}
	})
}
