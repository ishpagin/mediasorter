package scan

import (
	"database/sql"
	"fmt"
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
	ra, rb := voiceOf(t, d, a), voiceOf(t, d, b)
	if ra.suggest.Valid || rb.suggest.Valid || !ra.person.Valid || rb.person != ra.person || ra.person.Int64 == 1 {
		t.Fatalf("rejected suggestion: want one new unnamed person, no suggestion: %+v %+v", ra, rb)
	}

	// an automatic unnamed person keeps its voices and still gets a suggestion; marks of a gone person are dropped
	mustExec(t, d, "DELETE FROM voice_not_person")
	mustExec(t, d, "INSERT INTO voice_not_person (voice_id, person_id) VALUES (?, 999)", a)
	if err := ClusterVoices(d, testVoiceOptions()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{a, b} {
		if r := voiceOf(t, d, id); r.person != ra.person || r.suggest.Int64 != 1 {
			t.Fatalf("voice %d: want its unnamed person %v and Петя?, got %+v", id, ra.person, r)
		}
	}
	var orphans int
	if err := d.QueryRow("SELECT COUNT(*) FROM voice_not_person").Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("orphan 'not his voice' marks: %d %v", orphans, err)
	}
}

// vectors are compared after normalizing: small ones still group
func TestClusterVoicesUnnormalized(t *testing.T) {
	d := voicesDB(t, 2)
	var ids []int64
	for i, v := range [][]float32{{0.3, 0, 0, 0}, {0.3, 0.03, 0, 0}} {
		res, err := d.Exec("INSERT INTO voices (file_id, label, seconds, model, emb) VALUES (?, 's1', 10, 'm', ?)", i+1, F16Blob(v))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	if err := ClusterVoices(d, testVoiceOptions()); err != nil {
		t.Fatal(err)
	}
	if r1, r2 := voiceOf(t, d, ids[0]), voiceOf(t, d, ids[1]); r1.cluster != r2.cluster || !r1.person.Valid || r1.person != r2.person {
		t.Fatalf("unnormalized vectors not grouped: %+v %+v", r1, r2)
	}
}

// the program never moves a voice from one named person to another: that would join two people
func TestNamedAutoStays(t *testing.T) {
	d := voicesDB(t, 3)
	mustExec(t, d, "INSERT INTO persons (id, name) VALUES (8, 'Маша'), (20, NULL)")
	a := voice(t, d, 1, []float32{1, 0, 0, 0}, 20, 0)
	b := voice(t, d, 2, []float32{0.99, 0.1, 0, 0}, 20, 0)
	c := voice(t, d, 3, []float32{0.98, 0, 0.1, 0}, 8, 0)
	if err := ClusterVoices(d, testVoiceOptions()); err != nil {
		t.Fatal(err)
	}
	ra, rb, rc := voiceOf(t, d, a), voiceOf(t, d, b), voiceOf(t, d, c)
	if ra.cluster != rc.cluster || ra.person.Int64 != 20 || rb.person.Int64 != 20 || rc.person.Int64 != 8 {
		t.Fatalf("named voice moved: %+v %+v %+v", ra, rb, rc)
	}
}

// The user marks a voice while ClusterVoices runs: a trigger turns it manual on the write step's first
// update, after the voices were read as automatic. The mark must not be overwritten.
func TestClusterVoicesManualMidway(t *testing.T) {
	d := voicesDB(t, 3)
	mustExec(t, d, "INSERT INTO persons (id, name) VALUES (9, 'Вера')")
	a1 := voice(t, d, 1, []float32{1, 0, 0, 0}, nil, 0)
	voice(t, d, 2, []float32{0.99, 0.1, 0, 0}, nil, 0)
	voice(t, d, 3, []float32{0.98, 0, 0.1, 0}, nil, 0)
	mustExec(t, d, fmt.Sprintf(`CREATE TRIGGER mark AFTER UPDATE ON voices WHEN OLD.id = %d AND NEW.manual = 0 AND NEW.cluster IS NULL
		BEGIN UPDATE voices SET person_id = 9, manual = 1 WHERE id = OLD.id; END`, a1))
	if err := ClusterVoices(d, testVoiceOptions()); err != nil {
		t.Fatal(err)
	}
	var manual int
	d.QueryRow("SELECT manual FROM voices WHERE id = ?", a1).Scan(&manual)
	if r := voiceOf(t, d, a1); r.person.Int64 != 9 || manual != 1 || r.suggest.Valid {
		t.Fatalf("manual mark overwritten: %+v manual=%d", r, manual)
	}
}

func TestRefineVoiceGroups(t *testing.T) {
	E := [][]float32{
		{1, 0, 0, 0}, {1, 0.1, 0, 0}, {0, 0, 0, 1}, // 0: two alike + a stranger chained in
		{1, 0, 0.4, 0}, {1, 0, 0.5, 0}, // 1: the same voice split off
		{0, 1, 0, 0}, {0, 1, 0.1, 0}, // 2
		{0, 1, 0, 0.3}, {0, 1, 0, 0.2}, // 3: like 2, but marked "different"
		{0, 0, 1, 0}, {0, 0, 1, 0.05}, // 4: Маша by hand
		{0, 0, 1, 0.2}, {0, 0, 1, 0.25}, // 5: Петя by hand
	}
	for _, v := range E {
		normalize(v)
	}
	labels := []int{0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5}
	owner := []int64{-1, -1, -1, -1, -1, -1, -1, -1, -1, 7, 7, 9, -1}
	notSame := map[[2]int]bool{{5, 7}: true, {7, 5}: true}
	got := refineVoiceGroups(E, labels, notSame, owner, DefaultVoiceOptions())
	if got[0] != got[1] || got[3] != got[0] || got[4] != got[0] {
		t.Fatalf("split voice not joined: %v", got)
	}
	for i, l := range got {
		if i != 2 && l == got[2] {
			t.Fatalf("stranger stays in a group: %v", got)
		}
	}
	if got[5] == got[7] || got[9] == got[11] {
		t.Fatalf("joined across a 'different' mark or two manual people: %v", got)
	}
}
