package spatial

import (
	"testing"
)

func TestIsResidential(t *testing.T) {
	tests := []struct {
		code     string
		name     string
		expected bool
	}{
		{"R-1", "", true},
		{"", "Residential - Single Family", true},
		{" RM ", "", true},
		{"C-1", "Commercial", false},
		{"", "", false},
	}

	for _, tt := range tests {
		result := IsResidential(tt.code, tt.name)
		if result != tt.expected {
			t.Errorf("IsResidential('%s', '%s') = %v, expected %v", tt.code, tt.name, result, tt.expected)
		}
	}
}
