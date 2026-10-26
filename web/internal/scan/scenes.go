package scan

import (
	"database/sql"
	"math"
	"sort"

	"github.com/ishpagin/mediasorter/web/internal/vision"
)

// Category: a label you see and the prompts that describe it (English prompts work best).
type Category struct {
	Label   string
	Prompts []string
}

// Categories: the scene labels, in the Python order (scenes.py CATEGORIES).
var Categories = []Category{
	{"Море и пляж", []string{"a photo of a beach", "a photo of the sea", "people swimming in the sea"}},
	{"Горы", []string{"a photo of mountains", "a mountain landscape", "hiking in the mountains"}},
	{"Лес и природа", []string{"a photo of a forest", "a nature landscape", "a field and trees", "rocks and a cave",
		"the ground with plants", "a park with trees and paths", "autumn leaves", "mushrooms"}},
	{"Река и озеро", []string{"a photo of a river", "a photo of a lake", "a waterfall"}},
	{"Город и улицы", []string{"a city street", "buildings in a city", "a town square"}},
	{"Достопримечательности", []string{"a famous landmark", "a monument", "a church or cathedral", "a museum exhibit"}},
	{"Дом и интерьер", []string{"a room interior", "a kitchen", "a living room", "a bedroom", "a bathroom", "a bed",
		"a wall with wallpaper", "a hallway"}},
	{"Еда и напитки", []string{"a photo of food", "a dish on a plate", "drinks in glasses", "a restaurant table with food",
		"food cooking on a grill"}},
	{"Торт и праздничный стол", []string{"a birthday cake with candles", "a festive dinner table"}},
	{"Новый год", []string{"a christmas tree", "new year decorations", "new year celebration"}},
	{"Документы и текст", []string{"a document", "a sheet of paper with text", "a passport or id card", "a contract",
		"a hand-drawn sketch on paper"}},
	{"Чеки и квитанции", []string{"a receipt", "a bill or invoice"}},
	{"Скриншот", []string{"a screenshot of a phone screen", "a screenshot of an app", "a screenshot of a chat"}},
	{"Картинки и мемы", []string{"a meme", "a cartoon picture", "a digital illustration", "a poster or advertisement"}},
	{"Кошки", []string{"a photo of a cat", "a kitten"}},
	{"Собаки", []string{"a photo of a dog", "a puppy"}},
	{"Другие животные", []string{"a photo of a horse", "a photo of birds", "farm animals", "a wild animal"}},
	{"Цветы и растения", []string{"a photo of flowers", "a garden with plants", "a bouquet"}},
	{"Автомобили", []string{"a photo of a car", "a car interior", "a car dashboard"}},
	{"Поездки и транспорт", []string{"an airplane", "a view from an airplane window", "a train", "an airport", "a bus",
		"a road", "a view from a car window", "a highway"}},
	{"Закат и небо", []string{"a sunset", "a sunrise", "the sky with clouds"}},
	{"Ночь и огни", []string{"city lights at night", "fireworks", "a night scene"}},
	{"Зима и снег", []string{"snow in winter", "a snowy landscape"}},
	{"Концерт и мероприятие", []string{"a concert stage", "a crowd at an event", "a performance"}},
	{"Спорт", []string{"people playing sports", "a gym", "a stadium"}},
	{"Дети и игрушки", []string{"children's toys", "a playground", "a child playing"}},
	{"Ремонт и стройка", []string{"a construction site", "home renovation", "tools and building materials"}},
	{"Вещи и покупки", []string{"a product photo", "electronics", "clothes", "items for sale", "a product package",
		"cosmetics bottles and tubes", "a snack package", "medicine", "a wallet or a bag", "goods on a store shelf"}},
	{"Портрет и селфи", []string{"a selfie", "a portrait of a person", "a person posing for a photo"}},
	{"Группа людей", []string{"a group photo of people", "friends together", "a family gathering"}},
	{"Неудачный кадр", []string{"a completely black image", "an extremely blurry photo with motion blur",
		"a photo taken accidentally inside a pocket", "an overexposed white image"}},
	{"Прочее", []string{"a photo", "an everyday snapshot", "a random photo"}}, // catch-all fallback
}

var peopleCategories = map[string]bool{"Портрет и селфи": true, "Группа людей": true}

const otherCategory = "Прочее"

// FileEmbeddings: the file ids (ascending) and their mean scene vectors (videos average their previews).
func FileEmbeddings(db *sql.DB) ([]int64, [][]float32, error) {
	rows, err := db.Query("SELECT file_id, emb FROM clip ORDER BY file_id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var M [][]float32
	for rows.Next() {
		var fid int64
		var b []byte
		if err := rows.Scan(&fid, &b); err != nil {
			return nil, nil, err
		}
		v := Vectors(b, vision.ClipDim)
		if len(v) == 0 {
			continue
		}
		if n := len(ids); n > 0 && ids[n-1] == fid {
			for k, x := range v[0] {
				M[n-1][k] += x
			}
			continue
		}
		ids, M = append(ids, fid), append(M, v[0])
	}
	for _, m := range M {
		normalize(m)
	}
	return ids, M, rows.Err()
}

// Embed: scene vectors for previews that have none (files scanned without them, e.g. by an older version).
func Embed(db *sql.DB, clip *vision.Clip, progress Progress) (int, error) {
	rows, err := db.Query(`SELECT p.rowid, p.file_id, p.t_sec FROM previews p
		WHERE NOT EXISTS (SELECT 1 FROM clip c WHERE c.file_id = p.file_id) ORDER BY p.file_id`)
	if err != nil {
		return 0, err
	}
	type item struct {
		rowid, fid int64
		t          sql.NullFloat64
	}
	var todo []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.rowid, &it.fid, &it.t); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, it)
	}
	rows.Close()
	for i, it := range todo {
		var jpg []byte
		if err := db.QueryRow("SELECT jpeg FROM previews WHERE rowid = ?", it.rowid).Scan(&jpg); err != nil {
			return i, err
		}
		v, err := clip.ImageJPEG(jpg)
		if err != nil {
			continue // a broken preview: no vector, the file is still listed
		}
		var t any
		if it.t.Valid {
			t = it.t.Float64
		}
		if _, err := db.Exec("INSERT INTO clip (file_id, t_sec, emb) VALUES (?,?,?)", it.fid, t, F16Blob(v)); err != nil {
			return i, err
		}
		if i%200 == 0 {
			logf(progress, i, len(todo), "scene vectors: %d of %d", i, len(todo))
		}
	}
	return len(todo), nil
}

// Tag: zero-shot scene categories for every file (top 3 with probabilities), replacing the old tags.
func Tag(db *sql.DB, text *vision.Clip) error {
	const temperature, otherPenalty = 100.0, 1.5
	T := make([][]float32, len(Categories))
	for i, c := range Categories {
		v, err := text.TextMean(c.Prompts)
		if err != nil {
			return err
		}
		T[i] = v
	}
	fids, M, err := FileEmbeddings(db)
	if err != nil || len(fids) == 0 {
		return err
	}
	faces := map[int64]bool{}
	rows, err := db.Query("SELECT id FROM files WHERE n_faces > 0")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		faces[id] = true
	}
	rows.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM tags"); err != nil {
		return err
	}
	ins, err := tx.Prepare("INSERT INTO tags (file_id, rank, label, score) VALUES (?,?,?,?)")
	if err != nil {
		return err
	}
	defer ins.Close()
	logits := make([]float64, len(Categories))
	order := make([]int, len(Categories))
	for i, fid := range fids {
		mx := math.Inf(-1)
		for j, c := range Categories {
			l := temperature * float64(dot(M[i], T[j]))
			if peopleCategories[c.Label] && !faces[fid] { // no faces: a back view of a person must not win
				l = math.Inf(-1)
			}
			if c.Label == otherCategory { // generic prompts match everything a bit: a fallback, not a competitor
				l -= otherPenalty
			}
			logits[j] = l
			mx = math.Max(mx, l)
		}
		var sum float64
		for j := range logits {
			logits[j] = math.Exp(logits[j] - mx)
			sum += logits[j]
			order[j] = j
		}
		sort.SliceStable(order, func(a, b int) bool { return logits[order[a]] > logits[order[b]] })
		for r := 0; r < 3; r++ {
			j := order[r]
			if _, err := ins.Exec(fid, r+1, Categories[j].Label, logits[j]/sum); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
