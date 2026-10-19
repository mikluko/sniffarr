package rot13

import "testing"

func TestString(t *testing.T) {
	for in, want := range map[string]string{"Hello, World 42": "Uryyb, Jbeyq 42", "abc|xyz": "nop|klm", "Чужой": "Чужой"} {
		if got := String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
		if back := String(String(in)); back != in {
			t.Errorf("String(String(%q)) = %q", in, back)
		}
	}
}
