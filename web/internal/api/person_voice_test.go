package api

import "testing"

func TestPersonVoiceMode(t *testing.T) {
	mux, d := voiceServer(t,
		"INSERT INTO persons (id, name) VALUES (5, NULL)",
		`INSERT INTO voices (id, file_id, label, seconds, model, person_id, suggest_id) VALUES
			(1, 1, 's1', 9, 'm', 5, 1), (2, 2, 's1', 9, 'm', 5, 1), (3, 2, 's2', 9, 'm', 2, NULL)`,
		`INSERT INTO speech (file_id, t_start, t_end, text, low, voice_id) VALUES
			(1, 10, 40, 'длинная', 'длинная', 1), (1, 20, 25, 'привет', 'привет', 1), (2, 0.5, 4, 'ну', 'ну', 2)`,
		"UPDATE persons SET n_voices = 2, n_files = 2 WHERE id = 5")

	var info PersonInfo
	if c := do(mux, "GET", "/api/person/5", "", &info); c != 200 || info.Voices != 2 || info.Faces != 0 || info.Suggest == nil || info.Suggest.ID != 1 {
		t.Fatalf("info %d %+v", c, info)
	}
	var files FilesResp
	if c := do(mux, "GET", "/api/person/5/files?mode=voice", "", &files); c != 200 || len(files.Items) != 2 || files.HiddenVideo != 0 {
		t.Fatalf("files %d %+v", c, files)
	}
	if it := files.Items[0]; it.Note != `"привет"` || it.At != 19 || files.Items[1].At != 0 {
		t.Fatalf("phrase: %+v %+v", it, files.Items[1])
	}

	if c := do(mux, "POST", "/api/person/5/voices/reject", `{"person":1}`, nil); c != 200 {
		t.Fatal(c)
	}
	info = PersonInfo{}
	do(mux, "GET", "/api/person/5", "", &info)
	if info.Suggest != nil {
		t.Errorf("suggestion stays after no: %+v", info.Suggest)
	}

	if c := do(mux, "POST", "/api/person/5/voices/unlink", `{"files":[2]}`, nil); c != 200 {
		t.Fatal(c)
	}
	var p2, p3 any
	var marks int
	d.QueryRow("SELECT person_id FROM voices WHERE id = 2").Scan(&p2)
	d.QueryRow("SELECT person_id FROM voices WHERE id = 3").Scan(&p3)
	d.QueryRow("SELECT COUNT(*) FROM voice_not_person WHERE voice_id = 2 AND person_id = 5").Scan(&marks)
	if p2 != nil || p3 != int64(2) || marks != 1 {
		t.Errorf("unlink: voice 2 %v, other person's voice %v, marks %d", p2, p3, marks)
	}
}
