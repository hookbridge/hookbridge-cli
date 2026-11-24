package termsafe

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClean(t *testing.T) {
	assert.Equal(t, "plain text é", Clean("plain text é"))
	assert.Equal(t, "a[31mb", Clean("a\x1b[31mb"))
	assert.Equal(t, "ab", Clean("a\u009bb"))
	assert.Equal(t, "ab", Clean("a\x7fb"))
	assert.Equal(t, "a b", Clean("a\nb"))
	assert.Equal(t, "a b", Clean("a\tb"))
	assert.Equal(t, "ab", Clean("a\rb"))
}
