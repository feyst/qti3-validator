package xmlenc

import "testing"

func TestToUTF8(t *testing.T) {
	le := []byte{0xFF, 0xFE, '<', 0, 'a', 0, 0xAC, 0x20, '/', 0, '>', 0}
	be := []byte{0xFE, 0xFF, 0, '<', 0, 'a', 0x20, 0xAC, 0, '/', 0, '>'}
	for _, in := range [][]byte{le, be, []byte("<a€/>")} {
		got, err := ToUTF8(in)
		if err != nil || string(got) != "<a€/>" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
}
