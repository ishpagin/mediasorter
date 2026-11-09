package scan

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ishpagin/mediasorter/web/internal/db"
)

func voicesDB(t *testing.T, files int) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= files; i++ {
		mustExec(t, d, "INSERT INTO files (id, path, kind) VALUES (?, ?, 'video')", i, filepath.Join("/v", string(rune('a'+i))))
	}
	return d
}

func mustExec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// voice adds a voice with a 4-dim vector (normalized here); returns its id
func voice(t *testing.T, d *sql.DB, file int64, v []float32, person any, manual int) int64 {
	t.Helper()
	normalize(v)
	res, err := d.Exec("INSERT INTO voices (file_id, label, seconds, model, emb, person_id, manual) VALUES (?, 's1', 10, 'm', ?, ?, ?)",
		file, F16Blob(v), person, manual)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

type voiceRow struct {
	cluster, person, suggest sql.NullInt64
}

func voiceOf(t *testing.T, d *sql.DB, id int64) voiceRow {
	t.Helper()
	var r voiceRow
	if err := d.QueryRow("SELECT cluster, person_id, suggest_id FROM voices WHERE id = ?", id).Scan(&r.cluster, &r.person, &r.suggest); err != nil {
		t.Fatal(err)
	}
	return r
}

func testVoiceOptions() VoiceOptions {
	o := DefaultVoiceOptions()
	o.Thr = 0.9
	return o
}

func TestClusterVoices(t *testing.T) {
	d := voicesDB(t, 6)
	mustExec(t, d, "INSERT INTO persons (id, name) VALUES (7, 'Маша')")
	a1 := voice(t, d, 1, []float32{1, 0, 0, 0}, nil, 0)
	a2 := voice(t, d, 2, []float32{0.99, 0.1, 0, 0}, nil, 0)
	a3 := voice(t, d, 3, []float32{0.98, 0, 0.1, 0}, nil, 0)
	lone := voice(t, d, 4, []float32{0, 1, 0, 0}, nil, 0)
	m := voice(t, d, 5, []float32{0, 0, 1, 0}, 7, 1)
	mn := voice(t, d, 6, []float32{0, 0.05, 1, 0}, nil, 0)
	x := voice(t, d, 1, []float32{0, 0, 0, 1}, nil, 0)
	y := voice(t, d, 2, []float32{0.05, 0, 0, 1}, nil, 0)
	mustExec(t, d, "INSERT INTO voice_not_same (a, b) VALUES (?, ?)", x, y)

	for run := 0; run < 2; run++ { // the second run keeps the same people
		if err := ClusterVoices(d, testVoiceOptions()); err != nil {
			t.Fatal(err)
		}
		r1, r2, r3 := voiceOf(t, d, a1), voiceOf(t, d, a2), voiceOf(t, d, a3)
		if !r1.person.Valid || r2.person != r1.person || r3.person != r1.person || r2.cluster != r1.cluster {
			t.Fatalf("run %d: three close voices: %+v %+v %+v", run, r1, r2, r3)
		}
		var name sql.NullString
		var nv, nf int
		if err := d.QueryRow("SELECT name, n_voices, n_files FROM persons WHERE id = ?", r1.person.Int64).Scan(&name, &nv, &nf); err != nil {
			t.Fatal(err)
		}
		if name.Valid || nv != 3 || nf != 3 {
			t.Fatalf("run %d: new person name %v n_voices %d n_files %d", run, name, nv, nf)
		}
		if r := voiceOf(t, d, lone); r.person.Valid {
			t.Fatalf("run %d: a lone voice got a person: %+v", run, r)
		}
		if r := voiceOf(t, d, m); r.person.Int64 != 7 {
			t.Fatalf("run %d: manual voice changed: %+v", run, r)
		}
		if r := voiceOf(t, d, mn); r.person.Int64 != 7 {
			t.Fatalf("run %d: neighbour of a manual voice: %+v", run, r)
		}
		rx, ry := voiceOf(t, d, x), voiceOf(t, d, y)
		if rx.cluster == ry.cluster || rx.person.Valid || ry.person.Valid {
			t.Fatalf("run %d: 'different' pair grouped: %+v %+v", run, rx, ry)
		}
	}
	var people int
	if err := d.QueryRow("SELECT COUNT(*) FROM persons").Scan(&people); err != nil {
		t.Fatal(err)
	}
	if people != 2 {
		t.Fatalf("people: %d, want Маша and one new", people)
	}
}

func TestSuggestVoice(t *testing.T) {
	d := voicesDB(t, 2)
	mustExec(t, d, "INSERT INTO persons (id, name) VALUES (1, 'Петя')")
	mustExec(t, d, "INSERT INTO faces (file_id, person_id, embedding) VALUES (1, 1, x'00'), (2, 1, x'00')")
	a := voice(t, d, 1, []float32{1, 0, 0, 0}, nil, 0)
	b := voice(t, d, 2, []float32{0.99, 0.1, 0, 0}, nil, 0)
	if err := ClusterVoices(d, testVoiceOptions()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a, b} {
		if r := voiceOf(t, d, id); r.suggest.Int64 != 1 || r.person.Valid {
			t.Fatalf("voice %d: want Петя? as a suggestion, got %+v", id, r)
		}
	}

	mustExec(t, d, "INSERT INTO voice_not_person (voice_id, person_id) VALUES (?, 1), (?, 1)", a, b)
	if err := ClusterVoices(d, testVoiceOptions()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a, b} {
		if r := voiceOf(t, d, id); r.suggest.Valid {
			t.Fatalf("voice %d: 'not his voice' still suggested: %+v", id, r)
		}
	}
}
