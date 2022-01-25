package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStringMapString(t *testing.T) {
	stringMap := StringMap(map[string]string{
		"master": "true",
	})
	expected := "master=true"

	if stringMap.String() != expected {
		t.Errorf("expected %s, got %s", expected, stringMap.String())
	}

}

func TestSetStringMapValue(t *testing.T) {
	for _, tc := range []struct {
		msg      string
		value    string
		valid    bool
		expected map[string]string
	}{
		{
			msg:   "test valid stringMap",
			value: "key=value",
			valid: true,
			expected: map[string]string{
				"key": "value",
			},
		},
		{
			msg:   "test invalid stringMap",
			value: "key:value",
			valid: false,
		},
		{
			msg:   "test valid stringMap with 'complex' key",
			value: "complex/key/1:2:3=value",
			valid: true,
			expected: map[string]string{
				"complex/key/1:2:3": "value",
			},
		},
	} {
		t.Run(tc.msg, func(t *testing.T) {
			stringMap := StringMap(map[string]string{})
			err := stringMap.Set(tc.value)
			if err != nil && tc.valid {
				t.Errorf("should not fail: %s", err)
			}

			if err == nil && !tc.valid {
				t.Error("expected failure")
			}

			if tc.valid {
				require.Equal(t, tc.expected, map[string]string(stringMap))
			}
		})
	}
}

func TestStringMapIsCumulative(t *testing.T) {
	var stringMap StringMap
	if !stringMap.IsCumulative() {
		t.Error("expected IsCumulative = true")
	}
}
