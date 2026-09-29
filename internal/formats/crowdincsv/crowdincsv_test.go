package crowdincsv

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	rows := []Row{
		{"40", "Новости", "Навіны", "Gameplay-Devices"},
		{"41@male", "Строка, с \"кавычками\"\nи переносом", "", ""},
	}
	var buf bytes.Buffer
	if err := Write(&buf, rows); err != nil {
		t.Fatal(err)
	}
	got, err := Read(&buf)
	if err != nil || !reflect.DeepEqual(got, rows) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestReadBOMAndBadHeader(t *testing.T) {
	if _, err := Read(strings.NewReader("\xEF\xBB\xBFid,source,translation,context\n1,a,b,c\n")); err != nil {
		t.Errorf("BOM header rejected: %v", err)
	}
	if _, err := Read(strings.NewReader("key,value\n")); err == nil {
		t.Error("bad header accepted")
	}
}
