package qrc

import "testing"

func TestDecodeNodeQRCFixture(t *testing.T) {
	const encoded = "bb1db48dc814ca5278a2b50c54638a6d528e00fa0c3a4ae612fc23c7755107bd504d97c24b81679c"
	actual, err := Decode(encoded)
	if err != nil || actual != "[0,1000]字(0,300)词(300,700)" {
		t.Fatalf("Decode = %q, %v", actual, err)
	}
	if _, err := Decode(encoded[:len(encoded)-2]); err == nil {
		t.Fatal("accepted a truncated cipher block")
	}
}
