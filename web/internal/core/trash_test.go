package core

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestTrashListPruneEmpty(t *testing.T) {
	e := setup(t)
	a := e.file("Photos/a.jpg", 1)
	b := e.file("Photos/b.jpg", 2)
	_, _, ids, err := e.cfg.Trash(e.db, []string{a, b})
	must(t, err)
	must(t, PushUndo(e.db, "s", "delete", "удаление: 2 файла", ids))
	must(t, PushUndo(e.db, "s", "faces", "лица", []int{7}))

	items, _, err := TrashList(e.db)
	must(t, err)
	if len(items) != 2 || items[0].Name != "b.jpg" || items[0].Kind != "image" || !items[0].InQuar || !items[0].Preview {
		t.Fatalf("list: %+v", items)
	}
	jpeg, err := TrashPreview(e.db, items[0].ID)
	must(t, err)
	if !bytes.Equal(jpeg, []byte{0xff, 0xd8, 2}) {
		t.Fatalf("preview: %v", jpeg)
	}

	// a.jpg vanished from the quarantine by hand: prune forgets it, the undo step still has b
	qa := filepath.Join(e.archive, QuarantineDir, "Photos", "a.jpg")
	must(t, os.Remove(qa))
	n, err := PruneTrash(e.db)
	must(t, err)
	if n != 1 || e.count("SELECT COUNT(*) FROM deleted") != 1 || e.count("SELECT COUNT(*) FROM undo_log WHERE kind = 'delete'") != 1 {
		t.Fatalf("prune: n %d", n)
	}

	// emptying erases b (and its copy in a share recycle bin), the dead undo step goes, other steps stay
	qb := filepath.Join(e.archive, QuarantineDir, "Photos", "b.jpg")
	rec := filepath.Join(e.archive, "#recycle", QuarantineDir, "Photos", "b.jpg")
	must(t, os.MkdirAll(filepath.Dir(rec), 0o775))
	must(t, os.WriteFile(rec, []byte("x"), 0o664))
	done, failed, err := EmptyTrash(e.db)
	must(t, err)
	if done != 1 || len(failed) != 0 || exists(qb) || exists(rec) {
		t.Fatalf("empty: done %d failed %v quarantine %v recycle %v", done, failed, exists(qb), exists(rec))
	}
	if e.count("SELECT COUNT(*) FROM deleted") != 0 || e.count("SELECT COUNT(*) FROM undo_log WHERE kind = 'delete'") != 0 ||
		e.count("SELECT COUNT(*) FROM undo_log WHERE kind = 'faces'") != 1 {
		t.Fatal("empty: rows or undo steps left wrong")
	}
}
