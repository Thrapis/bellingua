package tm

import (
	"context"
	"testing"

	"github.com/Thrapis/bellingua/internal/model"
)

func tx(s string) []model.Piece { return []model.Piece{{Text: s}} }

func TestSearch(t *testing.T) {
	ix := newIndex()
	ix.Put(1, tx("Поговорить с Антоном о письме"))
	ix.Put(2, tx("Поговорить с Олегом о брейнданс"))
	ix.Put(3, tx("Купить новый пистолет"))
	ix.Put(4, []model.Piece{{Text: "Поговорить с "}, {Text: "<b>", Code: true}, {Text: "Антоном"}, {Text: "</>", Code: true}, {Text: " о письме!"}})

	got := ix.Search(tx("Поговорить с Антоном о письме."), 0, 5)
	if len(got) < 2 || got[0].Score != 99 || got[1].Score != 99 {
		t.Fatalf("same words, different punctuation/markup should score 99: %+v", got)
	}
	var sawJudy bool
	for _, m := range got {
		if m.UnitID == 3 {
			t.Errorf("unrelated string matched: %+v", m)
		}
		if m.UnitID == 2 {
			sawJudy = true
			if m.Score != 60 { // 2 of 5 words differ
				t.Errorf("Judy score = %d, want 60", m.Score)
			}
		}
	}
	if !sawJudy {
		t.Errorf("60%% match missing: %+v", got)
	}
	if got := ix.Search(tx("Поговорить с Антоном о письме"), 1, 5); len(got) == 0 || got[0].UnitID == 1 {
		t.Errorf("except not honoured: %+v", got)
	}
}

func TestPutRemoveReplace(t *testing.T) {
	ix := newIndex()
	ix.Put(1, tx("Старый текст задания"))
	ix.Put(1, tx("Совсем другая строка"))
	if got := ix.Search(tx("Старый текст задания"), 0, 5); len(got) != 0 {
		t.Errorf("stale posting matched: %+v", got)
	}
	ix.Remove(1)
	if got := ix.Search(tx("Совсем другая строка"), 0, 5); len(got) != 0 {
		t.Errorf("removed unit matched: %+v", got)
	}
}

func TestManagerBuildsOnceAndUpdates(t *testing.T) {
	loads := 0
	m := NewManager(func(_ context.Context, _ int64, fn func(int64, []model.Piece) error) error {
		loads++
		return fn(1, tx("Взломать терминал"))
	})
	ix, _ := m.Get(context.Background(), 7)
	m.Get(context.Background(), 7)
	if loads != 1 || ix.Len() != 1 {
		t.Fatalf("loads=%d len=%d", loads, ix.Len())
	}
	m.Update(7, 2, tx("Взломать камеру"), true)
	if ix.Len() != 2 {
		t.Errorf("update not applied")
	}
	m.Update(8, 3, tx("x"), true) // not built: ignored, no panic
	m.Invalidate(7)
	m.Get(context.Background(), 7)
	if loads != 2 {
		t.Errorf("invalidate did not rebuild: loads=%d", loads)
	}
}
