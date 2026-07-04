package geelark

import "testing"

func TestFindClickableCenterForText(t *testing.T) {
	xml := `<hierarchy>
<node index="0" text="" clickable="true" bounds="[0,0][100,100]" />
<node index="1" text="Hapus" clickable="true" bounds="[912,1894][1032,1966]" />
<node index="2" text="OpenAI LLC" content-desc="" clickable="true" bounds="[100,500][980,620]" />
</hierarchy>`
	x, y, ok := findClickableCenterForText(xml, "Hapus")
	if !ok || x != 972 || y != 1930 {
		t.Fatalf("Hapus: ok=%v x=%d y=%d", ok, x, y)
	}
	x, y, ok = findClickableCenterForText(xml, "OpenAI")
	if !ok || x != 540 || y != 560 {
		t.Fatalf("OpenAI: ok=%v x=%d y=%d", ok, x, y)
	}
	_, _, ok = findClickableCenterForText(xml, "TidakAda")
	if ok {
		t.Fatal("expected miss")
	}
}
